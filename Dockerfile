FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bubblewrap \
    socat \
    curl \
    git \
    python3 \
    python3-venv \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/claude-science

ARG CLAUDE_SCIENCE_DOWNLOAD_URL=""
RUN if [ -n "$CLAUDE_SCIENCE_DOWNLOAD_URL" ]; then \
        curl -fsSL -o /usr/local/bin/claude-science "$CLAUDE_SCIENCE_DOWNLOAD_URL" && \
        chmod +x /usr/local/bin/claude-science; \
    fi

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /opt/claude-science/api-bridge && \
    rm -rf /opt/claude-science/api-bridge/.git

RUN python3 -m venv /opt/claude-science/.venv && \
    .venv/bin/pip install --no-cache-dir --upgrade pip && \
    .venv/bin/pip install --no-cache-dir -r /opt/claude-science/api-bridge/requirements.txt && \
    cp /opt/claude-science/api-bridge/config.example.json /opt/claude-science/api-bridge/config.json && \
    chmod 600 /opt/claude-science/api-bridge/config.json

COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

WORKDIR /opt/claude-science/api-bridge

EXPOSE 9876

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["all"]
