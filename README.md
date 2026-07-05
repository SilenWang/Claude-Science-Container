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

- **MCP directory connectors unavailable**
-  **The `web_search` tool is unavailable**
- **Missing image interpretation config** — The current .env example does not explicitly configure image handling policies, which may cause unexpected behavior during runtime.

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


## Configuration

All configuration is done via environment variables in `.env`:

| Variable | Description | Default |
|----------|-------------|---------|
| `CUSTOM_API_KEY` | API key for the custom backend | — |
| `CUSTOM_BASE_URL` | API base URL (e.g., SiliconFlow, Moonshot) | — |
| `DEFAULT_BACKEND` | Default backend selection | `custom` |
| `FORCE_MODEL` | Force a specific model name | — |
| `CUSTOM_UPSTREAM_MODE` | Upstream protocol: `openai` or `anthropic` | `openai` |
| `INLINE_IMAGE_POLICY` | Image handling: `preserve`, `omit`, `omit_inline`, `auto` | `preserve` |
| `CUSTOM_MODEL_PATTERN` | Regex pattern for custom backend model matching | — |

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
