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
- **API Bridge** — Translates Anthropic-style API calls from Claude Science to OpenAI-compatible third-party endpoints (DeepSeek, SiliconFlow, Moonshot, etc.). Implemented in Go under `proxy/` (standard library only); its route and translation conventions follow [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208).

## API Bridge (Go)

The bridge lives in `proxy/` and uses only the Go standard library. The Docker image compiles it in a `golang:1.26` build stage and copies a single static binary into the runtime layer: nothing is cloned from the upstream Python project, no patches are applied, and no venv/pip dependencies exist in the image. Build and test it through pixi:

```bash
pixi run build_bridge   # builds proxy/api-bridge
pixi run test_bridge    # go test ./...
```

Configuration comes entirely from environment variables (`.env`).

`api-bridge setup-token` replaces the upstream `setup-token.py`. It reads `OAUTH_ENCRYPTION_KEY=` from `~/.claude-science/encryption.key`, encrypts a fake OAuth token with Fernet (AES-128-CBC + HMAC-SHA256, standard library only), and writes `~/.claude-science/.oauth-tokens/byok-user-000000000000000000.enc` (directory `0700`, file `0600`). It then reads the file back, decrypts it to verify, and prints `verified: token decrypts`.

## Current Status

The container successfully runs both claude-science and the API bridge. Key accomplishments:

- **API routing works** — Claude Science routes Anthropic-style API calls through the bridge, which translates them to OpenAI-compatible third-party endpoints. Models are callable via the web UI.
- **Environment-based configuration** — All API keys and backend options are read directly from environment variables by the Go bridge at startup; there is no `config.json` to rewrite.
- **OAuth token auto-setup** — On first run, the entrypoint lets claude-science create its encryption key if missing, then the Go bridge writes a Fernet-encrypted fake OAuth token. No upstream Python script is involved.
- **Multiple backend support** — Custom (SiliconFlow, Moonshot, etc.), DeepSeek, and OpenAI backends are all supported. **The default is the DeepSeek official API** with model `deepseek-v4-flash-vision-exp` (displayed as "DeepSeek V4 Flash"); it is a vision model, so image requests go to it directly.
- **Model menu and image input** — `GET /v1/models` serves the alias/built-in model list, and Anthropic image blocks (base64 or URL) are translated to OpenAI `image_url` parts according to `INLINE_IMAGE_POLICY`.

## Chinese Fonts in Plots

`fonts-noto-cjk` is installed in the image. On every boot, the entrypoint
idempotently patches the runtime conda env's matplotlib defaults
(`scripts/configure-cjk-fonts.py`) so Python plots render Chinese with Noto
Sans/Serif/Mono CJK instead of tofu boxes. The code-execution sandbox gives
each spawn a fresh per-workspace `MPLCONFIGDIR` and a tmpfs `$HOME`, so the
only matplotlib config read there is the packaged `matplotlibrc`, which the
script edits in place. R already resolves `sans` to Noto CJK via fontconfig,
so R plots are unaffected. The script no-ops until claude-science provisions
the conda env on first boot and re-applies on every later boot.

## Known Issues

I am not a professional software developer, so the reasons for some of the features below are based on speculation and are for reference only.

- **MCP directory connectors unavailable**: The error that appears upon entering the settings interface does not seem to cause any appreciable effect in practice. According to the literature review, bioinformatics analysis can be carried out normally.
-  **The `web_search` tool is unavailable**: Compatibility issue: OpenAI-compatible model endpoints do not provide the tool-calling capabilities of Anthropic-compatible endpoints, and therefore cannot use web search. For example, I encounter this issue when using DeepSeek in opencode-go, but when using the Anthropic-compatible endpoint of the official DeepSeek API, web search works normally.
- **Trailing text after a text block is dropped in streaming responses**: Once the bridge has already emitted a text block, any later text delta from the same upstream response is discarded, because Anthropic content blocks are closed in order. If the upstream sends `tool_calls` first and text afterwards, the bridge still opens a text block after the tool block, so that text is kept. Anthropic normally does not emit text after tool calls, so this only affects non-conforming upstreams.
- **Built-in model IDs fall back to `FORCE_MODEL`**: This is intentional — the model menu must stay clickable — and each `requested -> forced` pair logs one warning. See [Model mapping](#model-mapping-and-v1models).
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
   Edit `.env` and set your DeepSeek official API key. The default backend is DeepSeek official API:
   ```ini
   DEEPSEEK_API_KEY=sk-your-api-key-here
   DEFAULT_BACKEND=deepseek
   ```
   The default model is `deepseek-v4-flash-vision-exp` (shown as **DeepSeek V4 Flash** in the model selector), and image requests automatically fall back to the same DeepSeek vision model.

3. **Build and start**
   - **Without Nvidia GPU** (default):
     ```bash
     docker compose up -d
     ```
   - **With Nvidia GPU** (requires host with Nvidia driver + nvidia-container-toolkit, GPU is mounted into the container via CDI — see [GPU Support (via CDI)](#gpu-support-via-cdi)):
     ```bash
     docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
     ```
   Two compose configurations are provided:
   - `docker-compose.yml` — no GPU access (runs anywhere)
   - `docker-compose.gpu.yml` — CDI override that mounts the host GPU into the container via the CDI device `nvidia.com/gpu=all`, on top of the base file

4. **Open Claude Science**
   - Check the container logs for the login URL, you will find something like `http://localhost:9981/?nonce=token`, use this to enter webui of Claude Science in browser.


## GPU Support (via CDI)

The container supports Nvidia GPUs, but the image itself is **not** a CUDA-flavored Docker image (no `nvidia/cuda` base, no CUDA toolkit baked in). The Dockerfile is built on plain `ubuntu:24.04`. Instead, GPU access is provided at runtime by **mounting the host GPU into the container through CDI (Container Device Interface)**. The host's NVIDIA driver, driver libraries, and device nodes are exposed to the container via a CDI device spec, so GPU tools (`nvidia-smi`, CUDA) work inside the container without shipping a CUDA image.

### How the CDI device is declared

The `docker-compose.gpu.yml` override mounts the GPU by declaring the CDI device directly on the service:

```yaml
services:
  claude-science:
    devices:
      - nvidia.com/gpu=all
```

`nvidia.com/gpu=all` is a CDI-qualified device name. It is equivalent to passing `--device nvidia.com/gpu=all` (or `--gpus all`) to `docker run`; the GPU device is injected into the container by the CDI runtime rather than preinstalled in the image.

### Prerequisites on the host

1. **NVIDIA driver** installed on the host (kernel driver + `nvidia-smi` working).
2. **nvidia-container-toolkit** installed and Docker configured to use it:
   ```bash
   # Debian/Ubuntu
   sudo apt-get install -y nvidia-container-toolkit
   sudo nvidia-ctk runtime configure --runtime=docker
   sudo systemctl restart docker
   ```

### Using CDI to mount the GPU

CDI requires a device spec (`nvidia.yaml`) describing the host GPU. The nvidia-container-toolkit provides `nvidia-ctk` to generate it:

1. **Generate the CDI spec** (one time on the host):
   ```bash
   sudo nvidia-ctk cdi generate --output=/etc/cdi/nvidia.yaml
   ```
2. **Verify the CDI devices are visible**:
   ```bash
   nvidia-ctk cdi list
   # expected output: nvidia.com/gpu=0, nvidia.com/gpu=1, ..., nvidia.com/gpu=all
   ```

   If the `nvidia.com/gpu=all` entry is missing, the spec was not generated correctly — regenerate it before starting the container.

3. **Start the container with the GPU override**:
   ```bash
   docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d
   ```
4. **Sanity-check inside the container**:
   ```bash
   docker exec -it claude-science-dev nvidia-smi
   ```

   For a plain `docker run`, the equivalent invocation is:
   ```bash
   docker run --rm -it --device nvidia.com/gpu=all claude-science-container-dev nvidia-smi
   ```

You can also mount a specific GPU by index instead of all of them (e.g. `nvidia.com/gpu=0`).

### Enabling GPU passthrough into the code-execution sandbox

claude-science keeps GPU passthrough into its sandbox **off** unless `config.toml` sets `gpu_enabled = true`. The entrypoint writes it automatically based on the `ENABLE_GPU` env var in `.env`:

| `ENABLE_GPU` | Behavior |
|--------------|----------|
| `auto` (default) | Enable GPU when a GPU device is present in the container (i.e. mounted via CDI) |
| `true` | Always enable GPU passthrough |
| `false` | Never enable it |

When enabled, the sandbox gets `/dev/nvidia*` and `/sys/module/nvidia*` dev-bound into it. Note that this only applies to **new** sessions — sessions created before GPU was enabled keep their previous `gpu_mode`. CUDA/torch are not preinstalled in the sandbox's default Python env; install them as needed.


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
| `DEEPSEEK_UPSTREAM_MODE` | DeepSeek upstream protocol: `openai` (default); `anthropic` is accepted but logs a warning and is still treated as `openai` | `openai` |
| `OPENAI_API_KEY` | API key for OpenAI backend | — |
| `OPENAI_BASE_URL` | OpenAI API base URL | `https://api.openai.com` |
| `DEFAULT_BACKEND` | Default backend selection: `custom`, `deepseek`, `openai` | `deepseek` |
| `FORCE_MODEL` | Model used for requests that are not mapped by `MODEL_ALIASES` (alias mappings win; fallbacks are logged) | `deepseek-v4-flash-vision-exp` |
| `CUSTOM_UPSTREAM_MODE` | Custom upstream protocol: `openai` or `anthropic` | `openai` |
| `INLINE_IMAGE_POLICY` | Image handling: `preserve` (keep all), `omit` (drop all), `omit_inline` (drop base64 only), `auto` (omit on DeepSeek, preserve elsewhere) | `preserve` |
| `REASONING_CONTENT_POLICY` | Accepted but currently has no effect in the Go bridge | `never` |
| `MODEL_ALIASES` | JSON array of model aliases for multi-model support | `[{"id":"claude-haiku-4-5","display_name":"DeepSeek V4 Flash","backend":"deepseek","model":"deepseek-v4-flash-vision-exp"}]` |
| `MODEL_LIST_MODE` | `/v1/models` layout: `aliases`, `builtin`, `aliases_first`; empty = `aliases` when `MODEL_ALIASES` is set, otherwise `builtin` | — |
| `MODEL_MENU_STRATEGY` | Accepted but currently has no effect in the Go bridge | — |
| `CUSTOM_MODEL_PATTERN` | Regex pattern for custom backend model matching | — |
| `DEEPSEEK_MODEL_PATTERN` | Regex pattern for DeepSeek model matching | `deepseek\|deep-seek` |
| `OPENAI_MODEL_PATTERN` | Regex pattern for OpenAI model matching | `^(gpt-\|o1\|o3\|o4\|chatgpt)` |
| `IMAGE_FALLBACK_MODE` | Accepted but currently not implemented by the Go bridge | `auto` |
| `IMAGE_FALLBACK_BACKEND` | Accepted but currently not implemented by the Go bridge | `deepseek` |
| `IMAGE_FALLBACK_MODEL` | Accepted but currently not implemented by the Go bridge | `deepseek-v4-flash-vision-exp` |

## Backend Mode Comparison

The bridge supports two fundamentally different API modes. Choosing the right one depends on your needs:

| Feature | Custom / third-party (OpenAI-compatible) | DeepSeek official API (OpenAI-compatible) |
|---------|------------------------------------------|------------------------------------------|
| **Configuration** | `CUSTOM_API_KEY` + `CUSTOM_BASE_URL` | `DEEPSEEK_API_KEY` (+ optional `DEEPSEEK_BASE_URL`) |
| **Protocol conversion** | Anthropic → OpenAI (via bridge translation) | Anthropic → OpenAI (via bridge translation) |
| **Tool calls** | ✅ Supported through translation | ✅ Supported through translation |
| **Web search** | ❌ Not available | ❌ Not available |
| **Model selection** | More third-party providers (SiliconFlow, Moonshot, etc.) | Limited to DeepSeek official models |

### Custom OpenAI API (`DEFAULT_BACKEND=custom`)

When using third-party providers like SiliconFlow or Moonshot, the bridge converts Claude Science's Anthropic-format requests to OpenAI-compatible format. Tool calls (`tools` / `tool_use` / `tool_result`) are translated in both directions. Features that have no OpenAI-compatible equivalent are not available:

- The **web search** tool in Claude Science will not function
- Extended thinking is not available

**Recommended for**: Users who want access to a wide range of models from various providers and don't need web search.

### DeepSeek official API (`DEFAULT_BACKEND=deepseek`, `DEEPSEEK_UPSTREAM_MODE=openai`) *(default)*

The DeepSeek official API is OpenAI-compatible and goes through the same translation path as any other OpenAI-compatible upstream. `DEEPSEEK_UPSTREAM_MODE=anthropic` is accepted for backward compatibility, but the Go bridge logs a warning and still treats the upstream as OpenAI-compatible — there is no Anthropic pass-through.

- The **web search** tool in Claude Science will not function
- Tool calls (`tools` / `tool_use` / `tool_result`) are supported through translation
- Anthropic extended thinking is not forwarded

**Recommended for**: Users who want the DeepSeek official API with the default vision model and working tool calls.

### How to switch

```bash
# Option 1 (default): DeepSeek official API with vision model (tool support, no web search)
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=openai
# Default model deepseek-v4-flash-vision-exp is shown as "DeepSeek V4 Flash"
# (set via MODEL_ALIASES).

# Option 2: Custom OpenAI API (wider model selection, no web search)
CUSTOM_API_KEY=sk-xxx
CUSTOM_BASE_URL=https://api.siliconflow.cn
DEFAULT_BACKEND=custom
MODEL_ALIASES=
```

For the full environment-variable list, see `.env.example` and `proxy/config.go`. The route and translation conventions follow the [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) project.

## Ports

| Port | Service | Description |
|------|---------|-------------|
| `9876` | API Bridge | Proxy endpoint for third-party API translation |
| `9981` | Claude Science | Web UI for Claude Science |

## Data Persistence

Container data (Claude Science configuration and sessions) is stored in a Docker volume named `cs-data`, mounted at `/root/.claude-science`.

## Multi-Model Configuration (model_aliases)

When using the DeepSeek official API in Anthropic-compatible mode, you can configure multiple models (e.g., DeepSeek Flash and Pro) to appear in Claude Science's model selector simultaneously. This uses the bridge's `model_aliases` mechanism instead of `FORCE_MODEL`.

### How it works

Set `MODEL_ALIASES` as a JSON array where each entry maps a Claude-compatible ID to a real backend model. The bridge itself builds `GET /v1/models` from `MODEL_ALIASES` and `MODEL_LIST_MODE`; the entrypoint does **not** rewrite any model-list setting.

Claude Science's model selector will show the `display_name` from each alias, while actual requests use the mapped `model` value on the configured `backend`. Requests whose model is not an alias fall back to `FORCE_MODEL` (see [Model mapping](#model-mapping-and-v1models)).

### Configuration example

```ini
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=anthropic
MODEL_ALIASES='[{"id":"claude-haiku-4-5","display_name":"DeepSeek V4 Flash","backend":"deepseek","model":"deepseek-v4-flash-vision-exp"},{"id":"claude-sonnet-5","display_name":"DeepSeek V4 Pro","backend":"deepseek","model":"deepseek-v4-pro"}]'
```

With this configuration, the model selector shows **DeepSeek V4 Flash** and **DeepSeek V4 Pro** as two options. An alias always wins over `FORCE_MODEL`; only requests that match no alias fall back to it.

By default (no `MODEL_ALIASES` override), the container ships with a single alias that shows **DeepSeek V4 Flash** in the selector while sending `deepseek-v4-flash-vision-exp` to the DeepSeek official API.

### Alias entry fields

| Field | Description |
|-------|-------------|
| `id` | Claude-compatible model ID (e.g., `claude-sonnet-5`, `claude-haiku-4-5`) |
| `display_name` | Name shown in Claude Science's model selector |
| `backend` | Backend to use: `deepseek`, `openai`, or `custom` |
| `model` | The real model name sent to the API |

### Without multi-model (single model)

If you only need a single model, keep `FORCE_MODEL` and leave `MODEL_ALIASES` empty. `/v1/models` then lists the built-in model IDs, and every request is sent to `FORCE_MODEL`.

```ini
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=anthropic
FORCE_MODEL=deepseek-v4-flash-vision-exp
MODEL_ALIASES=
```

## Model mapping and /v1/models

For every `POST /v1/messages`, the bridge resolves the requested model in this order:

1. **Alias match** — if the ID equals an entry's `id` in `MODEL_ALIASES`, that alias's `backend` and `model` are used.
2. **Pattern fallback** — otherwise the backend stays `DEFAULT_BACKEND`, while `DEEPSEEK_MODEL_PATTERN` / `OPENAI_MODEL_PATTERN` / `CUSTOM_MODEL_PATTERN` can still select a different backend.
3. **Force fallback** — if no mapping produced a model name, `FORCE_MODEL` is used (when set); with `FORCE_MODEL` unset the requested ID passes through unchanged.

Claude Science's model menu contains both alias IDs and built-in IDs such as `gpt-4o`. A click on a model that is not an alias still reaches a working backend: it is sent as `FORCE_MODEL`. This is intentional — rejecting it would make the menu unusable — and the bridge logs one warning per distinct `requested -> forced` pair so the fallback stays observable.

`GET /v1/models` returns:

```json
{"data":[{"id":"claude-haiku-4-5","display_name":"DeepSeek V4 Flash","type":"model"}],"has_more":false,"first_id":"claude-haiku-4-5","last_id":"claude-haiku-4-5"}
```

`MODEL_LIST_MODE` selects what goes into `data`:

| `MODEL_LIST_MODE` | `data` contents |
|---|---|
| `aliases` (also `alias`, `third_party`) | only `MODEL_ALIASES` entries; an empty alias list yields an empty list |
| `builtin` (also `compat`) | only the built-in model list |
| `aliases_first` (also `mixed`) | alias entries first, then built-ins, de-duplicated by `id` |
| unset | `aliases` when `MODEL_ALIASES` is non-empty, otherwise `builtin` |

An empty list is still a valid response: `data: []`, `has_more: false`, and `first_id` / `last_id` are `null`.

## Image Handling

Anthropic `image` blocks become OpenAI `image_url` content parts at their original position in the message (text and images keep the order they have in the Anthropic `content` array):

- `source.type: "base64"` becomes `data:<media_type>;base64,<data>` — the `media_type` field supplies the MIME type and defaults to `image/png`.
- `source.type: "url"` passes the `url` through unchanged.

`INLINE_IMAGE_POLICY` decides which images are forwarded:

| Value | Behavior |
|---|---|
| `preserve` (default) | keep every image |
| `omit` | drop every image block |
| `omit_inline` | drop base64 inline images, keep `http(s)` URL images |
| `auto` | act like `omit` on the DeepSeek backend and `preserve` on the others |

When every image in a user message is dropped, the bridge appends a short placeholder text block instead of sending an empty user message, which OpenAI-compatible upstreams reject with HTTP 400.

`IMAGE_FALLBACK_MODE` / `IMAGE_FALLBACK_BACKEND` / `IMAGE_FALLBACK_MODEL` are still passed through by docker-compose but are **not implemented** in the Go bridge and currently have no effect. The default backend model (`deepseek-v4-flash-vision-exp`) is already a vision model, so images reach it directly without a fallback.

## Acknowledgements

This project's API bridge (`proxy/`) started from the route and translation conventions of [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208), and is now an independent Go implementation that uses only the standard library.

## License

MIT
