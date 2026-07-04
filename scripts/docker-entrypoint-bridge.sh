#!/usr/bin/env bash
set -euo pipefail

export PATH="/opt/api-bridge/.venv/bin:$PATH"

echo "Starting claude-science-api-bridge..."
exec python /opt/api-bridge/proxy.py
