#!/usr/bin/env bash
set -euo pipefail

if ! command -v claude-science &>/dev/null; then
    echo "ERROR: claude-science binary not found."
    echo "Provide via build arg CLAUDE_SCIENCE_DOWNLOAD_URL or mount to /usr/local/bin/claude-science"
    exit 1
fi

echo "Starting claude-science daemon..."
exec claude-science serve --no-browser --host 0.0.0.0 --port 9981
