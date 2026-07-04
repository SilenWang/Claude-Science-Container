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

setup_oauth

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/claude-science.conf
