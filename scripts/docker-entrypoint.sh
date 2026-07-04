#!/usr/bin/env bash
set -euo pipefail

CS_BIN="/usr/local/bin/claude-science"
CS_HOME="${HOME}/.claude-science"
ENC_KEY="${CS_HOME}/encryption.key"

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
    OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
    OPENAI_BASE_URL="${OPENAI_BASE_URL:-}" \
    CUSTOM_API_KEY="${CUSTOM_API_KEY:-}" \
    CUSTOM_BASE_URL="${CUSTOM_BASE_URL:-}" \
    DEFAULT_BACKEND="${DEFAULT_BACKEND:-custom}" \
    FORCE_MODEL="${FORCE_MODEL:-}" \
    CUSTOM_UPSTREAM_MODE="${CUSTOM_UPSTREAM_MODE:-openai}" \
    INLINE_IMAGE_POLICY="${INLINE_IMAGE_POLICY:-preserve}" \
    PROXY_HOST="0.0.0.0" \
    PROXY_PORT="9876" \
    "$py" - "$cfg" <<'PY'
import json, os, sys
path = sys.argv[1]
data = json.loads(open(path).read())
mapping = {
    "DEEPSEEK_API_KEY": "deepseek_api_key",
    "DEEPSEEK_BASE_URL": "deepseek_base_url",
    "OPENAI_API_KEY": "openai_api_key",
    "OPENAI_BASE_URL": "openai_base_url",
    "CUSTOM_API_KEY": "custom_api_key",
    "CUSTOM_BASE_URL": "custom_base_url",
    "DEFAULT_BACKEND": "default_backend",
    "FORCE_MODEL": "force_model",
    "CUSTOM_UPSTREAM_MODE": "custom_upstream_mode",
    "INLINE_IMAGE_POLICY": "inline_image_policy",
    "PROXY_HOST": "proxy_host",
    "PROXY_PORT": "proxy_port"
}
changed = []
for env_k, cfg_k in mapping.items():
    v = os.environ.get(env_k)
    if v:
        data[cfg_k] = int(v) if cfg_k == "proxy_port" else v
        changed.append(cfg_k)
if changed:
    open(path, "w").write(json.dumps(data, indent=2) + "\n")
    print(f"Applied config: {', '.join(changed)}")
PY
}

setup_oauth
apply_config

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/claude-science.conf
