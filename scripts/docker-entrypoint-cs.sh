#!/usr/bin/env bash
set -euo pipefail

CS_BIN="${CLAUDE_SCIENCE_BIN:-/usr/local/bin/claude-science}"
CS_HOME="${HOME}/.claude-science"
ENC_KEY="${CS_HOME}/encryption.key"

if ! command -v "$CS_BIN" &>/dev/null; then
    echo "ERROR: claude-science binary not found at $CS_BIN"
    echo "Provide binary via CLAUDE_SCIENCE_DOWNLOAD_URL build arg or mount to $CS_BIN"
    echo "Container will idle until binary is available..."
    sleep infinity
fi

if [ ! -f "$ENC_KEY" ]; then
    echo "First run: generating encryption.key..."
    "$CS_BIN" serve --no-browser --detached --port 9999 2>/dev/null || true
    sleep 3
    "$CS_BIN" stop 2>/dev/null || true
    sleep 1
fi

echo "Starting claude-science daemon..."
exec "$CS_BIN" serve --no-browser --host 0.0.0.0 --port 9981
