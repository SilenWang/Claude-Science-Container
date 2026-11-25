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
- **csh** (`claude-science-helper`) — The project's single Go binary, named `csh`. It contains the API bridge (`serve`), the OAuth token writer (`setup-token`), the remote SSH launcher (`forward`), the local all-in-one mode (`local`) and a desktop window with both modes as tabs (`gui`). The bridge source lives under `proxy/`; its route and translation conventions follow [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208).

## Build (single binary)

Everything compiles into one binary through pixi-managed Go:

```bash
pixi run build        # host binary: ./csh
pixi run build_all    # cross-compiled release binaries in dist/
pixi run test         # go test ./... (CLI, config, proxy and GUI view tests)
pixi run vet          # go vet ./...
pixi run screenshots  # render both GUI tabs to dist/screenshots (no display needed)
```

The Docker image compiles the same source in a `golang:1.27` build stage and copies one static binary into the runtime layer: nothing is cloned from the upstream Python project, no patches are applied, and no venv/pip dependencies exist in the image.

The container keeps using environment variables (`.env` → docker-compose → `serve`). The desktop commands (`forward` / `local`) use a single `config.json` next to the executable; `.env` is only a compatibility fallback for container runs.

`setup-token` replaces the upstream `setup-token.py`. It reads `OAUTH_ENCRYPTION_KEY=` from `~/.claude-science/encryption.key`, encrypts a fake OAuth token with Fernet (AES-128-CBC + HMAC-SHA256), and writes `~/.claude-science/.oauth-tokens/byok-user-000000000000000000.enc` (directory `0700`, file `0600`). It then reads the file back, decrypts it to verify, and prints `verified: token decrypts`.

## Current Status

The container successfully runs both claude-science and the API bridge. Key accomplishments:

- **API routing works** — Claude Science routes Anthropic-style API calls through the bridge, which translates them to OpenAI-compatible third-party endpoints. Models are callable via the web UI.
- **Environment-based configuration in the container** — Inside the image, `serve` still reads all API keys and backend options directly from environment variables at startup. On the desktop, the same binary reads `config.json` instead (see [CLI](#cli-single-binary)).
- **OAuth token auto-setup** — On first run, the entrypoint lets claude-science create its encryption key if missing, then the Go bridge writes a Fernet-encrypted fake OAuth token. No upstream Python script is involved.
- **Multiple backend support** — Custom (SiliconFlow, Moonshot, etc.), DeepSeek, and OpenAI backends are all supported. **The default is the DeepSeek official API** with model `deepseek-flash` (displayed as "DeepSeek Flash"). It is a text-only model, so image blocks are dropped by default (`INLINE_IMAGE_POLICY=omit_inline`) and replaced with a short placeholder note.
- **Model menu and image input** — `GET /v1/models` serves the alias/built-in model list, and Anthropic image blocks (base64 or URL) are translated to OpenAI `image_url` parts according to `INLINE_IMAGE_POLICY`. When the mapped model cannot read images, `IMAGE_FALLBACK_*` (desktop: `local.image_fallback`) routes the request to a vision backend instead of dropping the picture.

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
- **Built-in model IDs fall back to `FORCE_MODEL`**: A built-in ID such as `gpt-4o` is sent to its own backend when that backend has an API key, and otherwise to `DEFAULT_BACKEND` + `FORCE_MODEL`, with one warning per `requested -> forced` pair. This is intentional — the model menu must stay clickable. See [Model mapping](#model-mapping-and-v1models).
- **Tasks terminate unexpectedly**: Tasks running on DeepSeek Flash sometimes terminate unexpectedly mid-execution, whereas DeepSeek Pro experiences this issue far less often.

## Troubleshooting

### "Agent Failed: Your Claude session is no longer valid"

Claude Science prints that sign-out message for **any** 401 it sees on a model call, which hides two very different causes:

1. **Claude Science was not started by this tool.** The fake OAuth token only works while requests go to the bridge, and Claude Science finds the bridge through `ANTHROPIC_BASE_URL` at start-up. A `claude-science serve` you launched yourself has no such variable, so it calls `api.anthropic.com` with a fake bearer and gets a 401. Start it through `local` / the GUI's local tab, or export the variable yourself before starting it:
   ```bash
   export ANTHROPIC_BASE_URL=http://127.0.0.1:9876   # the bridge address from config.json
   claude-science serve --no-browser
   ```
   On macOS, a Claude Science process started before the bridge is gone keeps the old environment; stop it (`claude-science stop`) and start it again from the shell that exports the variable. `local` and the GUI's local tab now stop any daemon left running before they start their own, because `claude-science serve` attaches to an existing daemon and would otherwise keep an environment without the bridge address.
2. **The third-party backend rejected its API key.** The bridge logs `[bridge] stream upstream returned 401: ...` and now answers 502 instead of relaying the 401, so the UI shows the backend's own message (`backend rejected the API key (upstream 401): ...`) instead of a bogus sign-out prompt. Fix the key (`local --set-key <KEY> --backend <name>`, `config.json`, or the container's `.env`) and retry.

If neither applies, check the logs: `claude-science logs --tail 100` for the UI side and the GUI's local tab (or `docker compose logs claude-science`) for the `[bridge]` lines.

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
   The default model is `deepseek-flash` (shown as **DeepSeek Flash** in the model selector). It is a text-only model: pasted images are dropped by default and replaced with a placeholder note. To keep image input, point `IMAGE_FALLBACK_BACKEND` / `IMAGE_FALLBACK_MODEL` at a vision model and leave that backend on `upstream_mode=openai` (see [Image Handling](#image-handling)); until the fallback target is a vision model it stays inert, so the shipped defaults behave exactly as before. If you instead map requests directly to a vision model, set `INLINE_IMAGE_POLICY=preserve` — the fallback only handles models that cannot read images.

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


## CLI (single binary)

`csh` (`claude-science-helper`) is both the container's bridge and a desktop tool:

| Command | What it does |
|---------|--------------|
| *(none)* / `gui` | Open the desktop window with the 转发 and 启动本地的 Claude Science tabs (use `--cli` for the terminal launcher) |
| `forward` | SSH-forward a remote Claude Science, fetch its login URL, optionally open the browser |
| `local` | Write the fake OAuth token, start the proxy in-process, launch a local `claude-science serve` against it, print the login URL |
| `serve` | Run only the API bridge from environment variables (container / supervisord) |
| `setup-token` | Write the fake OAuth token from `~/.claude-science/encryption.key` |
| `config path\|show\|init` | Inspect or create the shared `config.json` |
| `version` | Print the version |

Run `csh <command> --help` for each command's flags. The container image installs it as `/usr/local/bin/csh` and keeps a `claude-science-container` symlink for older scripts.

### config.json: location and first run

- The file is **next to the executable**: `<dir-of-binary>/config.json`. `CSC_CONFIG=/path/to/config.json` overrides it (the `CSC_` prefix is kept for compatibility), and `config path` prints the resolved path.
- On the first `forward`, `local`, `config show`, GUI launch or `--cli` invocation the file is created automatically with every default filled in.
- It contains API keys, so it is written with mode `0600`.
- Legacy helper files remain readable: both a single-target object and `{"targets":[...]}` are accepted. The canonical shape is `{"forward": {...}, "local": {...}}`.
- `.env` is only read by the container's `serve`. `local` additionally falls back to `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `CUSTOM_API_KEY` (and the matching `*_BASE_URL`) when the config value is empty.

Example:

```json
{
  "forward": {
    "targets": [
      {
        "name": "prod",
        "ssh_host": "192.168.1.100",
        "ssh_port": 22,
        "ssh_user": "root",
        "ssh_key": "/home/me/.ssh/id_ed25519",
        "container_id": "claude-science",
        "command": "claude-science url",
        "port_forwards": [
          {"local": 9876, "remote": 9876},
          {"local": 9981, "remote": 9981}
        ]
      }
    ]
  },
  "local": {
    "default_backend": "deepseek",
    "force_model": "deepseek-flash",
    "model_aliases": [],
    "model_list_mode": "aliases",
    "inline_image_policy": "omit_inline",
    "image_fallback": {"mode": "auto", "backend": "deepseek", "model": "deepseek-flash"},
    "proxy_host": "127.0.0.1",
    "proxy_port": 9876,
    "claude_science_path": "claude-science",
    "claude_science_port": 9981,
    "open_browser": true,
    "backends": {
      "deepseek": {"api_key": "", "base_url": "https://api.deepseek.com/v1", "upstream_mode": "openai", "model_pattern": ""},
      "openai": {"api_key": "", "base_url": "https://api.openai.com/v1", "upstream_mode": "openai"},
      "custom": {"api_key": "", "base_url": "", "upstream_mode": "openai"}
    }
  }
}
```

`forward.targets[]` fields:

| Field | Description |
|-------|-------------|
| `name` | Target label used by `forward --target` |
| `ssh_host` | Remote server hostname/IP |
| `ssh_port` | SSH port (default: 22) |
| `ssh_user` | SSH user (default: root) |
| `ssh_key` | Path to SSH private key |
| `container_id` | Docker container name/ID; when set, `command` runs through `docker exec` |
| `command` | Remote command that prints the login URL (default: `claude-science url`) |
| `port_forwards` | List of `{local, remote}` mappings (default: 9876 and 9981) |

`local` fields:

| Field | Description | Default |
|-------|-------------|---------|
| `default_backend` | `deepseek`, `openai` or `custom` | `deepseek` |
| `force_model` | Model for requests without an alias/pattern match | `deepseek-flash` |
| `model_aliases` | Array of `{id, display_name, backend, model}` (same shape as `MODEL_ALIASES`) | `[]` |
| `model_list_mode` | `/v1/models` layout: `aliases`, `builtin`, `aliases_first` (empty = auto) | — |
| `inline_image_policy` | `omit_inline`, `omit`, `preserve` or `auto` | `omit_inline` |
| `image_fallback` | `{mode, backend, model}` for image requests the mapped model cannot read; `mode` is `auto` or `off`, empty `backend`/`model` = not configured (images follow `inline_image_policy`) | `auto` / `deepseek` / `deepseek-flash` |
| `proxy_host` / `proxy_port` | Where the in-process bridge listens | `127.0.0.1` / `9876` |
| `claude_science_path` | `claude-science` binary (name on `PATH`, absolute path, or empty for auto-detect) | `claude-science` |
| `claude_science_port` | Port for `claude-science serve` | `9981` |
| `open_browser` | Open the login URL automatically | `true` |
| `backends.<name>` | `{api_key, base_url, upstream_mode, model_pattern}` for `deepseek`, `openai`, `custom` | provider defaults |

Usage:

```bash
# first run, terminal: creates config.json and prints the path and next steps
./csh --cli

# desktop window (the default when the binary is started without a command)
./csh

# remote mode: fill in forward.targets[].ssh_host / ssh_key first
./csh forward
./csh forward --target prod --no-browser

# local mode: store a key, then run
./csh local --set-key sk-xxx --backend deepseek
./csh local
echo "$CUSTOM_API_KEY" | ./csh local --set-key - --backend custom

# inspect the effective config
./csh config show
```

`forward` keeps the historical behavior: it runs the remote command once, prints and (unless `--no-browser`) opens the login URL, keeps the port forwards alive, pings every 10 s and reconnects 3 s after a drop. `local` needs `claude-science` available through `PATH` or `local.claude_science_path`, and launches it with `ANTHROPIC_BASE_URL=http://127.0.0.1:<proxy_port>`.

When `claude_science_path` is empty or the bare name `claude-science`, a failed `PATH` lookup falls back to the installer's well-known locations — `~/.claude-science/bin/claude-science`, `~/.local/bin/claude-science`, and on macOS `/Applications/Claude Science.app/Contents/Resources/bin/claude-science`, `~/Applications/Claude Science.app/Contents/Resources/bin/claude-science`, `/opt/homebrew/bin`, `/opt/local/bin`, `/usr/local/bin`. This matters for the GUI: an app started from Finder/Dock inherits launchd's minimal `PATH`, so a binary that works in your shell is often invisible to the window. Set `local.claude_science_path` to an absolute path to skip the search.

### Manual Usage (without the binary)

If connecting via SSH directly:

```bash
ssh -L 9876:127.0.0.1:9876 -L 9981:127.0.0.1:9981 user@your-server
# Then fetch the URL from the container:
docker exec claude-science claude-science url
```

## GUI (desktop window)

Started without a command, the binary opens a window with two tabs. Both tabs edit the **same** `config.json` next to the executable: a save from the window is what `forward` / `local` read on their next run, and a file edited by hand (or by `config init`) appears after `重新加载配置` (Cmd/Ctrl+R). Fields the window does not show — `model_aliases`, `model_list_mode`, `inline_image_policy`, `image_fallback` — are preserved on save. Every save is atomic and keeps mode `0600`; starting either mode saves the draft first, so what runs is what you see.

### 转发 — remote forwarding

One card per `forward.targets[]` entry. The card shows the name, `user@host` and the tunnel state (idle, connecting, connected, failed); technical text stays folded behind 详细信息.

- **Card actions**: 连接 (opens the tunnel, fetches the remote login URL and opens it), 打开浏览器 (reopens that URL), 断开 / 停止, 编辑 (reveals the form).
- **Form**: `名称`, `SSH 主机`, `SSH 端口`, `SSH 用户`, `密钥路径`, `容器 ID` and `远端命令`. The container id wraps the command in `docker exec`, exactly like the CLI.
- **端口转发**: any number of `本地 → 远端` pairs, with 添加转发 / 移除 per row.
- **多 target**: 添加服务器 adds a card, 删除这台服务器 removes one; every card keeps its own tunnel, and unchanged cards keep running when another card is saved. The next `forward --target <name>` uses whatever this tab saved.

### 启动本地的 Claude Science — local stack

One form plus a running-state card at the top.

- **接口后端**: `DeepSeek`, `OpenAI` or `自定义（OpenAI 兼容）`, with an `API Key` and `Base URL` per backend (switching backends restores that backend's values). An empty key falls back to the matching `*_API_KEY` environment variable, as in the CLI.
- **模型与代理**: `强制模型` (`force_model`) and `代理端口` (`proxy_port`); the bridge still listens on `127.0.0.1`.
- **Claude Science**: `可执行文件路径` (name on `PATH` or absolute), `Web 端口` (`claude_science_port`) and `启动后自动打开浏览器`.
- **启动** saves, prepares `~/.claude-science` credentials, starts the in-process bridge and then `claude-science serve`; the card shows the login URL and offers 打开 Web UI. 停止 closes the process and the bridge together.

The GUI is a desktop convenience only; the container image keeps using `serve` with environment variables. For screenshots of both tabs without a display, run `pixi run screenshots` (writes `dist/screenshots/*.png`); `pixi run test_gui` runs the headless view and config round-trip tests alone.

## Configuration (container environment variables)

For the container, all configuration is done via environment variables in `.env` and read by `serve`:

| Variable | Description | Default |
|----------|-------------|---------|
| `CUSTOM_API_KEY` | API key for the custom backend | — |
| `CUSTOM_BASE_URL` | API base URL (e.g., SiliconFlow, Moonshot) | — |
| `DEEPSEEK_API_KEY` | API key for DeepSeek official API | — |
| `DEEPSEEK_BASE_URL` | DeepSeek API base URL | `https://api.deepseek.com` |
| `DEEPSEEK_UPSTREAM_MODE` | DeepSeek upstream protocol: `openai` (default); `anthropic` is accepted but logs a warning and is still treated as `openai`, and it suppresses the image fallback | `openai` |
| `OPENAI_API_KEY` | API key for OpenAI backend | — |
| `OPENAI_BASE_URL` | OpenAI API base URL | `https://api.openai.com` |
| `DEFAULT_BACKEND` | Default backend selection: `custom`, `deepseek`, `openai` | `deepseek` |
| `FORCE_MODEL` | Model used for requests that have no `MODEL_ALIASES` mapping and whose pattern-matched backend has no API key (alias mappings win; fallbacks are logged) | `deepseek-flash` |
| `CUSTOM_UPSTREAM_MODE` | Custom upstream protocol: `openai` or `anthropic` | `openai` |
| `INLINE_IMAGE_POLICY` | Image handling: `omit_inline` (drop base64 only), `omit` (drop all), `preserve` (keep all), `auto` (omit on DeepSeek, preserve elsewhere) | `omit_inline` |
| `REASONING_CONTENT_POLICY` | Accepted but currently has no effect in the Go bridge | `never` |
| `MODEL_ALIASES` | JSON array of model aliases for multi-model support | `[{"id":"claude-haiku-4-5","display_name":"DeepSeek Flash","backend":"deepseek","model":"deepseek-flash"}]` |
| `MODEL_LIST_MODE` | `/v1/models` layout: `aliases`, `builtin`, `aliases_first`; empty = `aliases` when `MODEL_ALIASES` is set, otherwise `builtin` | — |
| `MODEL_MENU_STRATEGY` | Accepted but currently has no effect in the Go bridge | — |
| `CUSTOM_MODEL_PATTERN` | Regex pattern for custom backend model matching | — |
| `DEEPSEEK_MODEL_PATTERN` | Regex pattern for DeepSeek model matching | `deepseek\|deep-seek` |
| `OPENAI_MODEL_PATTERN` | Regex pattern for OpenAI model matching | `^(gpt-\|o1\|o3\|o4\|chatgpt)` |
| `IMAGE_FALLBACK_MODE` | Image vision fallback: `auto` routes an image request to the fallback backend/model when the mapped model cannot read images; `off` never does | `auto` |
| `IMAGE_FALLBACK_BACKEND` | Backend (`deepseek`, `openai`, `custom`) that receives image requests the mapped model cannot read | `deepseek` |
| `IMAGE_FALLBACK_MODEL` | Vision model on that backend; the fallback only takes over when this model can read images | `deepseek-flash` |

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

**Recommended for**: Users who want the DeepSeek official API with the default text model and working tool calls.

### How to switch

```bash
# Option 1 (default): DeepSeek official API with the text model (tool support, no web search)
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=openai
# Default model deepseek-flash is shown as "DeepSeek Flash"
# (set via MODEL_ALIASES). It is text-only: images are dropped by default.

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

When using the DeepSeek official API, you can configure multiple models (e.g., DeepSeek Flash and Pro) to appear in Claude Science's model selector simultaneously. This uses the bridge's `model_aliases` mechanism instead of `FORCE_MODEL`.

### How it works

Set `MODEL_ALIASES` as a JSON array where each entry maps a Claude-compatible ID to a real backend model. The bridge itself builds `GET /v1/models` from `MODEL_ALIASES` and `MODEL_LIST_MODE`; the entrypoint does **not** rewrite any model-list setting.

Claude Science's model selector will show the `display_name` from each alias, while actual requests use the mapped `model` value on the configured `backend`. Requests whose model is not an alias are resolved by `*_MODEL_PATTERN` first and only fall back to `FORCE_MODEL` when that backend is not configured (see [Model mapping](#model-mapping-and-v1models)).

### Configuration example

```ini
DEEPSEEK_API_KEY=sk-xxx
DEFAULT_BACKEND=deepseek
DEEPSEEK_UPSTREAM_MODE=anthropic
MODEL_ALIASES='[{"id":"claude-haiku-4-5","display_name":"DeepSeek Flash","backend":"deepseek","model":"deepseek-flash"},{"id":"claude-sonnet-5","display_name":"DeepSeek V4 Pro","backend":"deepseek","model":"deepseek-v4-pro"}]'
```

With this configuration, the model selector shows **DeepSeek Flash** and **DeepSeek V4 Pro** as two options. An alias always wins over `FORCE_MODEL`; only requests that match no alias fall back to it.

`DEEPSEEK_UPSTREAM_MODE=anthropic` is accepted for compatibility but not implemented in M1 (the bridge only logs a warning and keeps talking OpenAI to the backend). One visible consequence: a backend in that mode is never rerouted by the image vision fallback, so images still follow `INLINE_IMAGE_POLICY`. Drop the line (or set `DEEPSEEK_UPSTREAM_MODE=openai`) when you want `IMAGE_FALLBACK_*` to carry images to a vision model.

By default (no `MODEL_ALIASES` override), the container ships with a single alias that shows **DeepSeek Flash** in the selector while sending `deepseek-flash` to the DeepSeek official API. Because that model is text-only, the default `INLINE_IMAGE_POLICY=omit_inline` drops inline images; switch both the model and the policy to a vision model if you need image input.

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
FORCE_MODEL=deepseek-flash
MODEL_ALIASES=
```

## Model mapping and /v1/models

For every `POST /v1/messages`, the bridge resolves the requested model in this order:

1. **Alias match** — if the ID equals an entry's `id` in `MODEL_ALIASES`, that alias's `backend` and `model` are used.
2. **Pattern routing** — otherwise `DEEPSEEK_MODEL_PATTERN` / `OPENAI_MODEL_PATTERN` / `CUSTOM_MODEL_PATTERN` can select a different backend. When the matched backend has an API key and base URL, the requested model ID is passed through unchanged: with `OPENAI_API_KEY` set, `gpt-4o` is sent to OpenAI as `gpt-4o`.
3. **Force fallback** — if no pattern matched (or the matched backend has no API key), the request goes to `DEFAULT_BACKEND` with `FORCE_MODEL` (when set); with `FORCE_MODEL` unset the requested ID passes through unchanged.

Claude Science's model menu contains both alias IDs and built-in IDs such as `gpt-4o` and `deepseek-flash`. A click on a model that is not an alias still reaches a working backend: without an `OPENAI_API_KEY`, `gpt-4o` falls back to `DEFAULT_BACKEND` + `FORCE_MODEL` (`deepseek-flash` by default). This is intentional — rejecting it would make the menu unusable — and the bridge logs one warning per distinct `requested -> forced` pair so the fallback stays observable.

`GET /v1/models` returns:

```json
{"data":[{"id":"claude-haiku-4-5","display_name":"DeepSeek Flash","type":"model"}],"has_more":false,"first_id":"claude-haiku-4-5","last_id":"claude-haiku-4-5"}
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
| `omit_inline` (default) | drop base64 inline images, keep `http(s)` URL images |
| `omit` | drop every image block |
| `preserve` | keep every image |
| `auto` | act like `omit` on the DeepSeek backend and `preserve` on the others |

The default model `deepseek-flash` is text-only, so a pasted image has no vision path: it is dropped and the model sees only the placeholder note. Set `preserve` only after pointing `FORCE_MODEL` / `MODEL_ALIASES` at a vision model.

When every image in a user message is dropped, the bridge appends a short placeholder text block instead of sending an empty user message, which OpenAI-compatible upstreams reject with HTTP 400.

### Vision fallback (`IMAGE_FALLBACK_*`)

When a request carries an image and the model it resolves to cannot read images, the bridge can send the whole request to a vision backend instead of letting `INLINE_IMAGE_POLICY` drop the picture. This is the Go port of the original Python bridge's `IMAGE_FALLBACK_*` variables; on the desktop the same three fields live in `local.image_fallback`.

| Variable (container) | `config.json` field | Meaning |
|---|---|---|
| `IMAGE_FALLBACK_MODE` | `image_fallback.mode` | `auto` (default) tries the fallback; `off` never reroutes |
| `IMAGE_FALLBACK_BACKEND` | `image_fallback.backend` | `deepseek`, `openai` or `custom`; must have a key and base URL |
| `IMAGE_FALLBACK_MODEL` | `image_fallback.model` | Vision model on that backend, e.g. `gpt-4o` |

`auto` decides per request:

1. A request without a forwardable `image` / `image_url` block goes to the resolved backend and model untouched.
2. With images, if the resolved model can read them, nothing changes.
3. Otherwise the request is rerouted to the fallback backend **and** fallback model, images are forwarded unchanged, and one log line records the hop:
   `[bridge] image fallback: deepseek/deepseek-flash -> openai/gpt-4o (requested=claude-haiku-4-5, images forwarded)`.
4. If the fallback is not configured, its backend has no key/base URL, or its model cannot read images either, nothing is rerouted and `INLINE_IMAGE_POLICY` applies exactly as before. This is why the shipped defaults (`auto` + `deepseek` + `deepseek-flash`) behave like they always did — `deepseek-flash` is text-only, so the fallback stays inert.

`/health` reports the state: `"image_fallback":{"mode":"auto","backend":"openai","model":"gpt-4o","active":true}`, or `"active":false` plus a `reason` such as `"fallback model deepseek-flash cannot read images"`.

How "can read images" is decided — the bridge does not probe upstreams, so it uses two rules:

- A backend configured with `upstream_mode = "anthropic"` is never rerouted. **M1 does not implement Anthropic pass-through**: that setting only logs a `not supported in M1` warning and the bridge still speaks OpenAI (`/chat/completions`) to the backend, so this rule effectively *suppresses* the fallback — images then follow `INLINE_IMAGE_POLICY` as before. Use `upstream_mode = "openai"` (the default) on the backend you want the fallback to route away from, and on the fallback backend itself.
- Every other model is judged by name: `gpt-4o`/`gpt-4.1`/`gpt-5`, `o1`/`o3`/`o4`, `claude-*`, `gemini*`, `glm-4v*`, `internvl*`, `llava*`, `pixtral*`, `minicpm-v`, `deepseek-vl*`, and any name containing `vision` or a `-vl` / `_vl` / `vlm` token. Everything else (including `deepseek-flash`) counts as text-only.

The fallback only fires when the model a request *resolves* to cannot read images. If you map requests straight to a vision model (`FORCE_MODEL=gpt-4o`, or an alias whose `model` is a vision model), nothing is rerouted and `INLINE_IMAGE_POLICY` still decides what happens to the images — with the default `omit_inline`, base64 images are dropped even though the model could read them. Pair such a mapping with `INLINE_IMAGE_POLICY=preserve`.

If you point the bridge at a vision model whose name is outside those families, set `IMAGE_FALLBACK_MODE=off` (or `image_fallback.mode: "off"`) together with `INLINE_IMAGE_POLICY=preserve`: `preserve` alone is enough to stop dropping, but `auto` would still judge the model by name.

### Priority with `INLINE_IMAGE_POLICY`

The two mechanisms never act on the same image twice:

| `INLINE_IMAGE_POLICY` | Fallback behavior |
|---|---|
| `preserve` | Wins outright: images go to the resolved model, the fallback never runs. This is the "send as-is, I know what I'm doing" switch. |
| `omit` / `omit_inline` (default) / `auto` | The fallback is tried first. When it takes over, images are forwarded unchanged for that request (the drop policy is not applied — no omission placeholder). When it does not, the policy applies with its usual semantics. |

### Images inside `tool_result`

Images nested in a `tool_result` block are flattened to text by the bridge, so they cannot be forwarded. They do not count as "the request has an image" and never trigger the fallback.

## Acknowledgements

This project's API bridge (`proxy/`) started from the route and translation conventions of [claude-science-api-bridge](https://github.com/Jyx0208/claude-science-api-bridge) by [Jyx0208](https://github.com/Jyx0208), and is now an independent Go implementation whose translation/routing logic uses only the standard library (SSH forwarding uses `golang.org/x/crypto/ssh`).

## License

MIT
