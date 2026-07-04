#!/usr/bin/env bash
set -euo pipefail

export PATH="/opt/api-bridge/.venv/bin:$PATH"
CS_HOME="${HOME}/.claude-science"
ENC_KEY="${CS_HOME}/encryption.key"

setup_oauth() {
    echo "Waiting for encryption.key from claude-science container..."
    for i in $(seq 1 30); do
        if [ -f "$ENC_KEY" ]; then
            echo "Generating OAuth token..."
            python /opt/api-bridge/setup-token.py 2>&1 || true
            return
        fi
        sleep 2
    done
    echo "WARNING: encryption.key not found after 60s. OAuth setup skipped."
}

setup_oauth

echo "Starting claude-science-api-bridge..."
exec python /opt/api-bridge/proxy.py
