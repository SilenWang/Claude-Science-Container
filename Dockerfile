ARG UBUNTU_VERSION=22.04

FROM ubuntu:${UBUNTU_VERSION} AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    python3 \
    python3-venv \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

ARG CLAUDE_SCIENCE_DOWNLOAD_URL=""
RUN if [ -n "$CLAUDE_SCIENCE_DOWNLOAD_URL" ]; then \
        echo "Downloading claude-science from $CLAUDE_SCIENCE_DOWNLOAD_URL" ; \
        mkdir -p /tmp/cs ; \
        curl -fsSL -o /tmp/cs/claude-science "$CLAUDE_SCIENCE_DOWNLOAD_URL" ; \
        chmod +x /tmp/cs/claude-science ; \
    fi

COPY claude-science-linux /build/claude-science-linux
RUN if [ -f /build/claude-science-linux ] && [ ! -f /tmp/cs/claude-science ]; then \
        mkdir -p /tmp/cs ; \
        cp /build/claude-science-linux /tmp/cs/claude-science ; \
        chmod +x /tmp/cs/claude-science ; \
    elif [ ! -f /tmp/cs/claude-science ]; then \
        echo "************************************************************************" ; \
        echo "WARNING: claude-science binary not provided." ; \
        echo "Place claude-science-linux in the build context, set" ; \
        echo "CLAUDE_SCIENCE_DOWNLOAD_URL build arg, or mount the binary at runtime" ; \
        echo "to /root/.local/bin/claude-science." ; \
        echo "Download from: https://claude.com/product/claude-science" ; \
        echo "************************************************************************" ; \
        mkdir -p /tmp/cs ; \
        touch /tmp/cs/.placeholder ; \
    fi

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /build/api-bridge && \
    rm -rf /build/api-bridge/.git

FROM ubuntu:${UBUNTU_VERSION}

ENV DEBIAN_FRONTEND=noninteractive
ENV PATH="/root/.local/bin:$PATH"
ENV CLAUDE_SCIENCE_HOME="/root/.claude-science"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    python3 \
    python3-venv \
    python3-pip \
    bubblewrap \
    socat \
    build-essential meson ninja-build libcap-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN bwrap_version=$(bwrap --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' || echo "0.0.0") && \
    if [ "$(printf '%s\n' "0.8.0" "$bwrap_version" | sort -V | head -n1)" != "0.8.0" ]; then \
        cd /tmp && \
        git clone --depth 1 --branch v0.8.0 https://github.com/containers/bubblewrap.git && \
        cd bubblewrap && \
        meson setup build && \
        ninja -C build && \
        ninja -C build install && \
        rm -rf /tmp/bubblewrap; \
    fi

COPY --from=builder /tmp/cs /tmp/cs-import
RUN mkdir -p /root/.local/bin && \
    if [ -f /tmp/cs-import/claude-science ]; then \
        cp /tmp/cs-import/claude-science /root/.local/bin/claude-science && \
        chmod +x /root/.local/bin/claude-science && \
        echo "claude-science installed"; \
    else \
        echo "claude-science binary not provided at build time - mount at runtime"; \
    fi && \
    rm -rf /tmp/cs-import

COPY --from=builder /build/api-bridge /opt/claude-science-api-bridge

RUN cd /opt/claude-science-api-bridge && \
    python3 -m venv .venv && \
    .venv/bin/pip install --no-cache-dir --upgrade pip && \
    .venv/bin/pip install --no-cache-dir -r requirements.txt && \
    if [ ! -f config.json ]; then \
        cp config.example.json config.json && \
        chmod 600 config.json; \
    fi

WORKDIR /opt/claude-science-api-bridge

COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 9876

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["all"]
