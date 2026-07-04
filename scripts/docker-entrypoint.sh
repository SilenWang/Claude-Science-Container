#!/usr/bin/env bash
set -euo pipefail

cd /opt/api-bridge

if command -v /usr/local/bin/claude-science &>/dev/null; then
    echo "Running install-safe.sh for config and OAuth setup..."
    VENV_DIR="/opt/api-bridge/.venv" \
    PYTHON="/opt/api-bridge/.venv/bin/python" \
    USE_SYSTEM_PYTHON=1 \
    PIP_REQUIRE_VIRTUALENV=0 \
    PROXY_HOST="0.0.0.0" \
    PROXY_PORT="9876" \
    bash scripts/install-safe.sh 2>&1 || true
else
    echo "WARNING: claude-science binary not found, OAuth setup skipped."
fi

exec /usr/bin/supervisord -c /etc/supervisor/conf.d/claude-science.conf
