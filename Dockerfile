ARG PIXI_IMAGE=ghcr.io/prefix-dev/pixi:0.72.0

FROM $PIXI_IMAGE AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY pixi.toml pixi.lock* /app/

ARG CLAUDE_SCIENCE_DOWNLOAD_URL=""
RUN if [ -n "$CLAUDE_SCIENCE_DOWNLOAD_URL" ]; then \
        mkdir -p /app/.local/bin && \
        curl -fsSL -o /app/.local/bin/claude-science "$CLAUDE_SCIENCE_DOWNLOAD_URL" && \
        chmod +x /app/.local/bin/claude-science; \
    fi

COPY claude-science-linux /app/claude-science-linux
RUN if [ -f /app/claude-science-linux ] && [ ! -f /app/.local/bin/claude-science ]; then \
        mkdir -p /app/.local/bin && \
        cp /app/claude-science-linux /app/.local/bin/claude-science && \
        chmod +x /app/.local/bin/claude-science; \
    fi

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /app/api-bridge && \
    rm -rf /app/api-bridge/.git

COPY scripts/docker-entrypoint.sh /app/docker-entrypoint.sh

RUN pixi install

RUN pixi shell-hook > /shell-hook.sh

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bubblewrap \
    socat \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/.pixi/envs/default /app/.pixi/envs/default
COPY --from=builder /shell-hook.sh /shell-hook.sh
COPY --from=builder /app/api-bridge /app/api-bridge
COPY --from=builder /app/.local/bin/claude-science /usr/local/bin/claude-science
COPY --from=builder /app/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV PIXI_ENV="/app/.pixi/envs/default"

WORKDIR /app/api-bridge

EXPOSE 9876

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["all"]
