ARG PIXI_IMAGE=ghcr.io/prefix-dev/pixi:0.72.0

FROM $PIXI_IMAGE

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bubblewrap \
    socat \
    curl \
    git \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY pixi.toml pixi.lock* /app/

ARG CLAUDE_SCIENCE_DOWNLOAD_URL=""
RUN if [ -n "$CLAUDE_SCIENCE_DOWNLOAD_URL" ]; then \
        mkdir -p /usr/local/bin && \
        curl -fsSL -o /usr/local/bin/claude-science "$CLAUDE_SCIENCE_DOWNLOAD_URL" && \
        chmod +x /usr/local/bin/claude-science; \
    fi

COPY claude-science-linux /app/claude-science-linux
RUN if [ -f /app/claude-science-linux ] && [ ! -f /usr/local/bin/claude-science ]; then \
        cp /app/claude-science-linux /usr/local/bin/claude-science && \
        chmod +x /usr/local/bin/claude-science; \
    fi && rm -f /app/claude-science-linux

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /app/api-bridge && \
    rm -rf /app/api-bridge/.git

RUN pixi install

RUN pixi shell-hook > /shell-hook.sh

COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

ENV PIXI_ENV="/app/.pixi/envs/default"

WORKDIR /app/api-bridge

EXPOSE 9876

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["all"]
