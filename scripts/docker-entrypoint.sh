#!/usr/bin/env bash
set -euo pipefail

OPERON_BIN="${OPERON_BIN:-/root/.local/bin/operon}"
API_BRIDGE_DIR="${API_BRIDGE_DIR:-/opt/claude-science-api-bridge}"

PROXY_HOST="${PROXY_HOST:-0.0.0.0}"
PROXY_PORT="${PROXY_PORT:-9876}"

apply_env_config() {
    cd "$API_BRIDGE_DIR"
    if [ ! -f config.json ]; then
        cp config.example.json config.json
        chmod 600 config.json
    fi

    local python_bin="$API_BRIDGE_DIR/.venv/bin/python"
    local PROJECT_DIR="$API_BRIDGE_DIR"

    CUSTOM_API_KEY="${CUSTOM_API_KEY:-}" \
    CUSTOM_BASE_URL="${CUSTOM_BASE_URL:-}" \
    DEFAULT_BACKEND="${DEFAULT_BACKEND:-}" \
    FORCE_MODEL="${FORCE_MODEL:-}" \
    CUSTOM_UPSTREAM_MODE="${CUSTOM_UPSTREAM_MODE:-openai}" \
    INLINE_IMAGE_POLICY="${INLINE_IMAGE_POLICY:-preserve}" \
    PROXY_HOST="$PROXY_HOST" \
    PROXY_PORT="$PROXY_PORT" \
    "$python_bin" - "$PROJECT_DIR" <<'PY'
import json
import os
from pathlib import Path

project_dir = Path(os.environ.get('PROJECT_DIR', '/opt/claude-science-api-bridge'))
config_path = project_dir / 'config.json'
data = json.loads(config_path.read_text())

mapping = {
    "CUSTOM_API_KEY": "custom_api_key",
    "CUSTOM_BASE_URL": "custom_base_url",
    "DEFAULT_BACKEND": "default_backend",
    "FORCE_MODEL": "force_model",
    "CUSTOM_UPSTREAM_MODE": "custom_upstream_mode",
    "INLINE_IMAGE_POLICY": "inline_image_policy",
    "PROXY_AUTH_TOKEN": "proxy_auth_token",
    "PROXY_AUTH_MODE": "proxy_auth_mode",
    "REASONING_CONTENT_POLICY": "reasoning_content_policy",
}

changed = []
for env_key, config_key in mapping.items():
    value = os.environ.get(env_key)
    if value:
        data[config_key] = value
        changed.append(config_key)

for env_key, config_key in {
    "CUSTOM_MODEL_MAP": "custom_model_map",
    "MODEL_ALIASES": "model_aliases",
    "MODEL_TOKEN_CAPS": "model_token_caps",
    "PROVIDER_PROFILES": "provider_profiles",
}.items():
    value = os.environ.get(env_key)
    if value:
        try:
            data[config_key] = json.loads(value)
            changed.append(config_key)
        except json.JSONDecodeError:
            pass

if "PROXY_HOST" in os.environ:
    data["proxy_host"] = os.environ["PROXY_HOST"]
if "PROXY_PORT" in os.environ:
    data["proxy_port"] = int(os.environ["PROXY_PORT"])

if changed:
    config_path.write_text(json.dumps(data, indent=2) + "\n")
    config_path.chmod(0o600)
    print(f"Applied config: {', '.join(changed)}")
PY
}

start_operon() {
    if [ ! -f "$OPERON_BIN" ]; then
        echo "WARNING: operon binary not found at $OPERON_BIN"
        echo "Download from https://claude.com/product/claude-science and mount to $OPERON_BIN"
        return 0
    fi

    echo "Starting Claude Science (operon)..."
    "$OPERON_BIN" serve --no-browser --detached 2>&1
    echo "operon daemon started."
}

start_api_bridge() {
    if [ ! -d "$API_BRIDGE_DIR" ]; then
        echo "ERROR: claude-science-api-bridge not found at $API_BRIDGE_DIR"
        exit 1
    fi

    PYTHON_BIN="$API_BRIDGE_DIR/.venv/bin/python"
    if [ ! -f "$PYTHON_BIN" ]; then
        echo "ERROR: Virtual environment not found. Rebuild the image."
        exit 1
    fi

    cd "$API_BRIDGE_DIR"

    apply_env_config

    echo "Starting claude-science-api-bridge on $PROXY_HOST:$PROXY_PORT..."
    exec "$PYTHON_BIN" proxy.py
}

case "${1:-all}" in
    operon)
        start_operon
        echo "operon exited. Container will stop."
        ;;
    bridge)
        start_api_bridge
        ;;
    all)
        start_operon
        start_api_bridge
        ;;
    shell)
        exec /bin/bash
        ;;
    *)
        echo "Usage: $0 {operon|bridge|all|shell}"
        exit 1
        ;;
esac
