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
        /opt/api-bridge/api-bridge setup-token 2>&1 || true
    fi
}

configure_plot_fonts() {
    # matplotlib defaults to DejaVu Sans (no CJK glyphs), so Chinese text in
    # Python plots renders as boxes even though fonts-noto-cjk is installed.
    # The claude-science code-execution sandbox forces MPLCONFIGDIR into a
    # fresh per-workspace cache and gives each spawn a tmpfs $HOME, so the
    # only matplotlibrc read inside the sandbox is the packaged one in the
    # conda python env. Patch it idempotently; on a fresh data dir the conda
    # env is provisioned by claude-science on first boot, so this no-ops
    # until then and re-applies on every later boot.
    local py="${CS_HOME}/conda/envs/python/bin/python"
    if [ ! -x "$py" ]; then
        echo "WARNING: conda python env not provisioned yet; skipping matplotlib CJK font config"
        return
    fi
    "$py" /usr/local/bin/configure-cjk-fonts.py 2>&1 || true
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
        # json_set 只改顶层帧的 _original_input.gpu_mode，其余字段与子帧保持原样。
        sqlite3 "$db" "UPDATE frames SET context_data = json_set(context_data, '\$._original_input.gpu_mode', 'on') WHERE parent_frame_id IS NULL AND json_extract(context_data, '\$._original_input.gpu_mode') = 'off';" 2>/dev/null || true
    fi
}

cleanup_proc_mounts

apply_gpu_config
setup_oauth
configure_plot_fonts

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/claude-science.conf
