#!/usr/bin/env bash
set -euo pipefail

CS_BIN="/usr/local/bin/claude-science"
CS_HOME="${HOME}/.claude-science"
ENC_KEY="${CS_HOME}/encryption.key"

cleanup_proc_mounts() {
    # NVIDIA CDI/container-toolkit mounts a tmpfs inside /proc (e.g.
    # /proc/driver/nvidia/params). A sub-mount underneath /proc breaks bwrap's
    # ability to remount proc for a nested PID namespace
    # ("bwrap: Can't mount proc on /newroot/proc: Operation not permitted"),
    # which degrades claude-science's sandbox to "PID-namespace isolation is
    # DEGRADED". Unmount any such sub-mounts before the app starts. The driver's
    # own /proc/driver/nvidia/* proc-files remain untouched, so GPU tooling
    # (nvidia-smi, CUDA) keeps working.
    local mounts mp
    mounts=$(awk '$2 ~ /^\/proc\// {print $2}' /proc/self/mounts)
    for mp in $(echo "$mounts" | sort -r); do
        umount "$mp" 2>/dev/null && echo "[entrypoint] unmounted proc sub-mount: $mp"
    done
}

setup_oauth() {
    if ! command -v "$CS_BIN" &>/dev/null; then
        echo "WARNING: claude-science binary not found. OAuth setup skipped."
        return
    fi

    if [ ! -f "$ENC_KEY" ]; then
        echo "First run: generating encryption.key..."
        "$CS_BIN" serve --no-browser --detached --port 9999 2>/dev/null || true
        sleep 3
        "$CS_BIN" stop 2>/dev/null || true
        sleep 1
    fi

    if [ -f "$ENC_KEY" ]; then
        echo "Generating OAuth token..."
        /opt/api-bridge/.venv/bin/python /opt/api-bridge/setup-token.py 2>&1 || true
    fi
}

apply_config() {
    local cfg="/opt/api-bridge/config.json"
    local py="/opt/api-bridge/.venv/bin/python"

    DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
    DEEPSEEK_BASE_URL="${DEEPSEEK_BASE_URL:-}" \
    DEEPSEEK_UPSTREAM_MODE="${DEEPSEEK_UPSTREAM_MODE:-openai}" \
    OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
    OPENAI_BASE_URL="${OPENAI_BASE_URL:-}" \
    CUSTOM_API_KEY="${CUSTOM_API_KEY:-}" \
    CUSTOM_BASE_URL="${CUSTOM_BASE_URL:-}" \
    DEFAULT_BACKEND="${DEFAULT_BACKEND:-custom}" \
    FORCE_MODEL="${FORCE_MODEL:-}" \
    CUSTOM_UPSTREAM_MODE="${CUSTOM_UPSTREAM_MODE:-openai}" \
    INLINE_IMAGE_POLICY="${INLINE_IMAGE_POLICY:-preserve}" \
    REASONING_CONTENT_POLICY="${REASONING_CONTENT_POLICY:-auto}" \
    DEEPSEEK_MODEL_PATTERN="${DEEPSEEK_MODEL_PATTERN:-}" \
    OPENAI_MODEL_PATTERN="${OPENAI_MODEL_PATTERN:-}" \
    CUSTOM_MODEL_PATTERN="${CUSTOM_MODEL_PATTERN:-}" \
    IMAGE_FALLBACK_MODE="${IMAGE_FALLBACK_MODE:-auto}" \
    IMAGE_FALLBACK_BACKEND="${IMAGE_FALLBACK_BACKEND:-}" \
    IMAGE_FALLBACK_MODEL="${IMAGE_FALLBACK_MODEL:-}" \
    MODEL_ALIASES="${MODEL_ALIASES:-}" \
    MODEL_LIST_MODE="${MODEL_LIST_MODE:-}" \
    MODEL_MENU_STRATEGY="${MODEL_MENU_STRATEGY:-}" \
    PROXY_HOST="0.0.0.0" \
    PROXY_PORT="9876" \
    "$py" - "$cfg" <<'PY'
import json, os, sys
path = sys.argv[1]
data = json.loads(open(path).read())
mapping = {
    "DEEPSEEK_API_KEY": "deepseek_api_key",
    "DEEPSEEK_BASE_URL": "deepseek_base_url",
    "DEEPSEEK_UPSTREAM_MODE": "deepseek_upstream_mode",
    "OPENAI_API_KEY": "openai_api_key",
    "OPENAI_BASE_URL": "openai_base_url",
    "CUSTOM_API_KEY": "custom_api_key",
    "CUSTOM_BASE_URL": "custom_base_url",
    "DEFAULT_BACKEND": "default_backend",
    "FORCE_MODEL": "force_model",
    "CUSTOM_UPSTREAM_MODE": "custom_upstream_mode",
    "INLINE_IMAGE_POLICY": "inline_image_policy",
    "REASONING_CONTENT_POLICY": "reasoning_content_policy",
    "DEEPSEEK_MODEL_PATTERN": "deepseek_model_pattern",
    "OPENAI_MODEL_PATTERN": "openai_model_pattern",
    "CUSTOM_MODEL_PATTERN": "custom_model_pattern",
    "IMAGE_FALLBACK_MODE": "image_fallback_mode",
    "IMAGE_FALLBACK_BACKEND": "image_fallback_backend",
    "IMAGE_FALLBACK_MODEL": "image_fallback_model",
    "MODEL_LIST_MODE": "model_list_mode",
    "MODEL_MENU_STRATEGY": "model_menu_strategy",
    "PROXY_HOST": "proxy_host",
    "PROXY_PORT": "proxy_port"
}
changed = []
for env_k, cfg_k in mapping.items():
    v = os.environ.get(env_k)
    if v:
        data[cfg_k] = int(v) if cfg_k == "proxy_port" else v
        changed.append(cfg_k)
aliases_json = os.environ.get("MODEL_ALIASES")
if aliases_json:
    try:
        aliases = json.loads(aliases_json)
        data["model_aliases"] = aliases
        data["model_list_mode"] = data.get("model_list_mode") or "aliases"
        data["model_menu_strategy"] = data.get("model_menu_strategy") or "claude_compatible"
        changed.append("model_aliases")
    except json.JSONDecodeError:
        print("WARNING: MODEL_ALIASES is not valid JSON, skipping")
if changed:
    open(path, "w").write(json.dumps(data, indent=2) + "\n")
    print(f"Applied config: {', '.join(changed)}")
PY
}

apply_gpu_config() {
    # claude-science keeps GPU passthrough into its sandbox OFF unless
    # config.toml sets gpu_enabled = true (it then --dev-binds /dev/nvidia*
    # and /sys/module/nvidia* into the code-execution sandbox). Enable it
    # whenever a GPU is present (or ENABLE_GPU=true), unless explicitly
    # disabled with ENABLE_GPU=false.
    local cfg="${CS_HOME}/config.toml"
    case "${ENABLE_GPU:-auto}" in
        false) return 0 ;;
        true) : ;;
        *) [ -e /dev/nvidiactl ] || return 0 ;;
    esac
    if [ ! -f "$cfg" ]; then
        printf 'gpu_enabled = true\n' > "$cfg"
    elif grep -qE '^[[:space:]]*gpu_enabled[[:space:]]*=' "$cfg"; then
        sed -i -E 's/^([[:space:]]*gpu_enabled[[:space:]]*=).*/\1 true/' "$cfg"
    else
        printf 'gpu_enabled = true\n' >> "$cfg"
    fi
    echo "[entrypoint] gpu_enabled=true written to $cfg"

    # Existing sessions created before GPU was enabled persist gpu_mode = "off"
    # in their root frame's context_data._original_input, which the daemon's
    # session-GPU resolver honours over gpu_enabled. Flip those to "on" so old
    # conversations also get sandbox GPU passthrough. Safe before daemon start.
    local db="${CS_HOME}/operon-cli.db"
    if [ -f "$db" ]; then
        /opt/api-bridge/.venv/bin/python - "$db" <<'PY' 2>/dev/null || true
import json, os, sqlite3, sys
db = sys.argv[1]
con = sqlite3.connect(db)
cur = con.cursor()
try:
    rows = cur.execute(
        "SELECT id, context_data FROM frames WHERE parent_frame_id IS NULL"
    ).fetchall()
except sqlite3.OperationalError:
    con.close()
    sys.exit(0)
for fid, cd in rows:
    try:
        o = json.loads(cd) if cd else {}
    except (TypeError, ValueError):
        continue
    oi = o.get("_original_input")
    if isinstance(oi, dict) and oi.get("gpu_mode") == "off":
        oi["gpu_mode"] = "on"
        cur.execute("UPDATE frames SET context_data=? WHERE id=?",
                    (json.dumps(o, ensure_ascii=False), fid))
        print(f"[entrypoint] session {fid[:8]} gpu_mode off->on")
con.commit()
con.close()
PY
    fi
}

cleanup_proc_mounts

apply_gpu_config
setup_oauth
apply_config

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/claude-science.conf
