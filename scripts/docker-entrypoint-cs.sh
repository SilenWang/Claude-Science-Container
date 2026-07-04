#!/usr/bin/env bash
set -euo pipefail

CS_BIN="${CLAUDE_SCIENCE_BIN:-/usr/local/bin/claude-science}"
CS_HOME="${HOME}/.claude-science"
ENC_KEY="${CS_HOME}/encryption.key"

setup_oauth() {
    if [ ! -f "$ENC_KEY" ]; then
        echo "First run: starting daemon briefly to generate encryption.key..."
        "$CS_BIN" serve --no-browser --detached --port 9999 2>/dev/null || true
        sleep 3
        "$CS_BIN" stop 2>/dev/null || true
        sleep 1
    fi

    if [ -f "$ENC_KEY" ]; then
        echo "Generating OAuth token for proxy..."
        python3 /opt/setup-token.py 2>&1 || true
    else
        echo "WARNING: encryption.key not found. OAuth setup skipped."
    fi
}

if ! command -v "$CS_BIN" &>/dev/null; then
    echo "ERROR: claude-science binary not found at $CS_BIN"
    echo "Provide via mount or build arg CLAUDE_SCIENCE_DOWNLOAD_URL"
    exit 1
fi

setup_oauth

echo "Starting claude-science daemon..."
exec "$CS_BIN" serve --no-browser --host 0.0.0.0 --port 9981
