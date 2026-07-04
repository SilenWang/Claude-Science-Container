#!/usr/bin/env bash
set -euo pipefail

eval "$(cat /shell-hook.sh)"

case "${1:-all}" in
    claude-science)
        exec claude-science serve --no-browser
        ;;
    bridge)
        exec python /app/api-bridge/proxy.py
        ;;
    all)
        if command -v claude-science &>/dev/null; then
            claude-science serve --no-browser --detached
        fi
        exec python /app/api-bridge/proxy.py
        ;;
    shell)
        shift
        exec /bin/bash "$@"
        ;;
    *)
        exec "$@"
        ;;
esac
