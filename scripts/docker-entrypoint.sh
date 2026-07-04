#!/usr/bin/env bash
set -euo pipefail

export PATH="/opt/claude-science/.venv/bin:$PATH"

case "${1:-all}" in
    claude-science)
        exec claude-science serve --no-browser
        ;;
    bridge)
        exec python /opt/claude-science/api-bridge/proxy.py
        ;;
    all)
        if command -v claude-science &>/dev/null; then
            claude-science serve --no-browser --detached
        fi
        exec python /opt/claude-science/api-bridge/proxy.py
        ;;
    shell)
        shift
        exec /bin/bash "$@"
        ;;
    *)
        exec "$@"
        ;;
esac
