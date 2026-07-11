# Claude-Science-Container

Run [Claude Science](https://claude.ai/science) Linux edition with a third-party API backend in a Docker container, eliminating the need for a direct Anthropic API key.

## Architecture

```
                 ┌─────────────────────────────────────────┐
                 │              Docker Container            │
                 │                                          │
                 │  ┌──────────────┐    ┌────────────────┐  │
                 │  │  API Bridge  │◄───│ Claude Science  │  │
                 │  │  (:9876)     │    │  (:9981)        │  │
                 │  └──────┬───────┘    └────────────────┘  │
                 │         │                                │
                 └─────────┼────────────────────────────────┘
                           │
              ┌────────────┴────────────┐
              │  Third-Party API (LLM)  │
              │  (e.g. SiliconFlow,     │
              │   Moonshot, DeepSeek)   │
              └─────────────────────────┘
```

- **claude-science** — The official Claude Science Linux binary, configured to route API calls through the internal API bridge.
- **API Bridge** — Translates Anthropic-style API calls from Claude Science to OpenAI-compatible third-party endpoints (DeepSeek, SiliconFlow, Moonshot, etc.). Based on [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208).

## Current Status

The container successfully runs both claude-science and the API bridge. Key accomplishments:

- **API routing works** — Claude Science routes Anthropic-style API calls through the bridge, which translates them to OpenAI-compatible third-party endpoints. Models are callable via the web UI.
- **Environment-based configuration** — All API keys and backend options are configurable via environment variables; the entrypoint script (`apply_config`) writes them into the bridge's `config.json` at startup.
- **OAuth token auto-setup** — On first run, the entrypoint generates an encryption key and OAuth token for claude-science.
- **Multiple backend support** — Custom (SiliconFlow, Moonshot, etc.), DeepSeek, and OpenAI backends are all supported.

## Known Issues

I am not a professional software developer, so the reasons for some of the features below are based on speculation and are for reference only.

- **MCP directory connectors unavailable**: The error that appears upon entering the settings interface does not seem to cause any appreciable effect in practice. According to the literature review, bioinformatics analysis can be carried out normally.
-  **The `web_search` tool is unavailable**: Compatibility issue: OpenAI-compatible model endpoints do not provide the tool-calling capabilities of Anthropic-compatible endpoints, and therefore cannot use web search. For example, I encounter this issue when using DeepSeek in opencode-go, but when using the Anthropic-compatible endpoint of the official DeepSeek API, web search works normally.
- **Missing image interpretation config**: The current .env example does not explicitly configure image handling policies, which may cause unexpected behavior during runtime.
- **Tasks terminate unexpectedly**: Tasks running on DeepSeek Flash sometimes terminate unexpectedly mid-execution, whereas DeepSeek Pro experiences this issue far less often.

## Prerequisites

## Quick Start

1. **Clone the repo**
   ```bash
   git clone https://github.com/SilenWang/Claude-Science-Container.git
   cd Claude-Science-Container
   ```

2. **Configure environment**
   ```bash
   cp .env.example .env
   ```
   Edit `.env` and set at least one API key. The recommended setup is a custom backend:
   ```ini
   CUSTOM_API_KEY=sk-your-api-key-here
   CUSTOM_BASE_URL=https://api.siliconflow.cn
   ```

3. **Build and start**
   ```bash
   docker compose up -d
   ```

4. **Open Claude Science**
   - Check the container logs for the login URL, you will find something like `http://localhost:9981/?nonce=token`, use this to enter webui of Claude Science in browser.


## Startup Assistant (helper)

A Go-based SSH launcher tool that connects to a remote server running the container, sets up port forwarding, automatically retrieves the login URL, and opens the browser.

### Quick Start

```bash
# Build (requires Go or use pixi)
pixi run build
# or: cd helper && CGO_ENABLED=0 go build -o launcher .

# Configure
cp helper/config.example.json helper/config.json
# Edit config.json with your SSH server details

# Run
./helper/launcher
```

### Configuration

| Field | Description |
|-------|-------------|
| `ssh_host` | Remote server hostname/IP |
| `ssh_port` | SSH port (default: 22) |
| `ssh_user` | SSH user (default: root) |
| `ssh_key` | Path to SSH private key |
| `container_id` | Docker container name/ID (default: claude-science) |
| `port_forwards` | List of `{local, remote}` port mappings |

The launcher automatically:
- Connects via SSH and sets up port forwarding (default: 9876, 9981)
- Fetches the Claude Science login URL from the container
- Opens the URL in your default browser
- Reconnects automatically on connection loss

### Manual Usage (without helper)

If connecting via SSH directly:

```bash
ssh -L 9876:127.0.0.1:9876 -L 9981:127.0.0.1:9981 user@your-server
# Then fetch the URL from the container:
docker exec claude-science claude-science url
```

## Configuration

All configuration is done via environment variables in `.env`:

| Variable | Description | Default |
|----------|-------------|---------|
| `CUSTOM_API_KEY` | API key for the custom backend | — |
| `CUSTOM_BASE_URL` | API base URL (e.g., SiliconFlow, Moonshot) | — |
| `DEEPSEEK_API_KEY` | API key for DeepSeek official API | — |
| `DEEPSEEK_BASE_URL` | DeepSeek API base URL | `https://api.deepseek.com` |
| `DEEPSEEK_UPSTREAM_MODE` | DeepSeek upstream protocol: `openai` or `anthropic` | `openai` |
| `OPENAI_API_KEY` | API key for OpenAI backend | — |
| `OPENAI_BASE_URL` | OpenAI API base URL | `https://api.openai.com` |
| `DEFAULT_BACKEND` | Default backend selection: `custom`, `deepseek`, `openai` | `custom` |
| `FORCE_MODEL` | Force a specific model name | — |
| `CUSTOM_UPSTREAM_MODE` | Custom upstream protocol: `openai` or `anthropic` | `openai` |
| `INLINE_IMAGE_POLICY` | Image handling: `preserve`, `omit`, `omit_inline`, `auto` | `preserve` |
| `REASONING_CONTENT_POLICY` | Reasoning content handling: `never`, `preserve`, `auto` | `never` |
| `CUSTOM_MODEL_PATTERN` | Regex pattern for custom backend model matching | — |
| `DEEPSEEK_MODEL_PATTERN` | Regex pattern for DeepSeek model matching | `deepseek\|deep-seek` |
| `OPENAI_MODEL_PATTERN` | Regex pattern for OpenAI model matching | `^(gpt-\|o1\|o3\|o4\|chatgpt)` |

## Backend Mode Comparison

The bridge supports two fundamentally different API modes. Choosing the right one depends on your needs:

| Feature | Custom (OpenAI style) | DeepSeek Anthropic style |
|---------|----------------------|--------------------------|
| **Configuration** | `CUSTOM_API_KEY` + `CUSTOM_BASE_URL` | `DEEPSEEK_API_KEY` + `DEEPSEEK_UPSTREAM_MODE=anthropic` |
| **Protocol conversion** | Anthropic → OpenAI (via bridge translation) | Pass-through (direct Anthropic API) |
| **Tool calls** | ❌ Unsupported — web search, native tool use not available | ✅ Fully supported |
| **Model selection** | More third-party providers (SiliconFlow, Moonshot, etc.) | Limited to DeepSeek official models |

### Custom OpenAI API (`DEFAULT_BACKEND=custom`)

When using third-party providers like SiliconFlow or Moonshot, the bridge converts Claude Science's Anthropic-format requests to OpenAI-compatible format. During this conversion, Anthropic-specific features — particularly **tool calls** — are lost. This means:

- The **web search** tool in Claude Science will not function
- Extended thinking is not available
- Only basic text generation and code execution work

**Recommended for**: Users who want access to a wide range of models from various providers and don't need web search.

### DeepSeek Anthropic API (`DEFAULT_BACKEND=deepseek`, `DEEPSEEK_UPSTREAM_MODE=anthropic`)

DeepSeek provides an official Anthropic-compatible API endpoint (`/anthropic/v1/messages`). When this mode is enabled, the bridge passes requests through **without format conversion**, preserving all Anthropic protocol features:

- The **web search** tool in Claude Science works normally
- Tool calls and function calling are fully supported
- All Anthropic message features are preserved

**Recommended for**: Users who need web search, tool use, or want the best compatibility with Claude Science features.

> ⚠️ **Note**: When using `DEEPSEEK_UPSTREAM_MODE=anthropic`, the `thinking` block is automatically stripped from requests since DeepSeek's Anthropic API does not support extended thinking (`thinking.type: "auto"` is not a valid value). This does not affect normal functionality.

### How to switch

```bash
# Option 1: Custom OpenAI API (wider model selection, no web search)
CUSTOM_API_KEY=sk-xxx
CUSTOM_BASE_URL=https://api.siliconflow.cn
DEFAULT_BACKEND=custom

# Option 2: DeepSeek Anthropic API (web search, tool support)
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=anthropic
```

For more detailed configuration instructions, please refer to the documentation in the [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge).

## Ports

| Port | Service | Description |
|------|---------|-------------|
| `9876` | API Bridge | Proxy endpoint for third-party API translation |
| `9981` | Claude Science | Web UI for Claude Science |

## Data Persistence

Container data (Claude Science configuration and sessions) is stored in a Docker volume named `cs-data`, mounted at `/root/.claude-science`.

## Acknowledgements

This project uses [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208) as the API translation layer. All credit for the bridge's backend configuration options and protocol translation logic goes to the original project.

## License

MIT
