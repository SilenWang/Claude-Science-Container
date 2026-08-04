# AGENTS.md

## What this is

A Docker container that runs [Claude Science](https://claude.ai/science) Linux binary alongside an API bridge that translates Anthropic-style API calls to OpenAI-compatible third-party backends (SiliconFlow, Moonshot, DeepSeek, etc.). No Anthropic API key needed.

## Dev workflow (no language toolchain on host)

Everything runs in Docker. No npm, cargo, pip, or lint/test commands.

```bash
cp .env.example .env           # edit with at least CUSTOM_API_KEY
docker compose up -d           # build & start
docker compose logs            # find login URL with nonce token
docker compose down            # stop
docker compose build --no-cache  # rebuild from scratch
```

## Architecture

- **Two processes** managed by supervisord inside the container:
  - `api-bridge` (port 9876) — Python proxy translating Anthropic → OpenAI API calls
  - `claude-science` (port 9981) — Web UI binary
- **Routing**: `ANTHROPIC_BASE_URL="http://127.0.0.1:9876"` set in supervisord config, not docker-compose.
- **`privileged: true`** required in docker-compose for bubblewrap sandbox.
- Data persisted in named volume `cs-data` at `/root/.claude-science`.

## Container entrypoint (`scripts/docker-entrypoint.sh`)

1. **OAuth auto-setup**: Briefly starts claude-science in detached mode to generate `encryption.key`, then stops it. Runs `setup-token.py` to create OAuth token.
2. **Config injection**: Inline Python reads env vars and writes them into `/opt/api-bridge/config.json` (maps `CUSTOM_API_KEY` → `custom_api_key`, etc.).
3. **Launches supervisord** with the two services.

## Configuration

All via `.env` → docker-compose env vars → entrypoint → bridge `config.json`.

| Variable | Purpose |
|---|---|
| `CUSTOM_API_KEY` + `CUSTOM_BASE_URL` | Primary backend (SiliconFlow, Moonshot) |
| `DEFAULT_BACKEND` | `custom`, `deepseek`, or `openai` |
| `FORCE_MODEL` | Override model name |
| `CUSTOM_UPSTREAM_MODE` | `openai` (default) or `anthropic` |
| `INLINE_IMAGE_POLICY` | `preserve` (default), `omit`, `omit_inline`, `auto` |
| `*_MODEL_PATTERN` | Regex for model matching per backend |

## Key files

- `Dockerfile` — Ubuntu 24.04, installs Python venv + bridge + claude-science binary
- `docker-compose.yml` — Single service, maps both ports, passes env vars, mounts `cs-data` volume
- `scripts/docker-entrypoint.sh` — OAuth setup → config injection → supervisord launch
- `scripts/supervisord.conf` — Defines the two managed processes
- `.env.example` — Template for all supported backend configs

## Notes

- Login URL with nonce token appears in container logs after startup.
- **GPU via CDI**: the NVIDIA toolkit injects a `tmpfs` mount at `/proc/driver/nvidia/params`. Any sub-mount under `/proc` breaks bwrap's nested-PID-namespace probe (`bwrap: Can't mount proc on /newroot/proc: Operation not permitted`), degrading the sandbox. The entrypoint's `cleanup_proc_mounts()` unmounts `/proc/*` sub-mounts before startup to fix this; `nvidia-smi`/CUDA still work (the driver's own `/proc/driver/nvidia/*` proc-files remain).
- **GPU inside the sandbox**: claude-science keeps sandbox GPU passthrough off unless `config.toml` sets `gpu_enabled = true` (it then `--dev-bind`s `/dev/nvidia*` + `/sys/module/nvidia{,uvm}` into the code-execution sandbox). The entrypoint's `apply_gpu_config()` writes it when a GPU is present or `ENABLE_GPU=true` (`auto`/`true`/`false` via `.env`). It applies to **new** sessions (existing frames keep their `gpu_mode`). CUDA/torch are NOT installed in the sandbox's default conda `python` env by default.
- Bridge source lives at `/opt/api-bridge` (cloned from `Jyx0208/claude-science-api-bridge`).
- claude-science binary downloaded from `downloads.claude.ai/claude-science/latest/linux-x64`.
- No test suite, no CI, no linter/formatter — this is a deploy-only project.
