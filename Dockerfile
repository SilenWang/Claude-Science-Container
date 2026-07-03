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

# Attempt to download operon binary if a URL is provided
ARG OPERON_DOWNLOAD_URL=""
RUN if [ -n "$OPERON_DOWNLOAD_URL" ]; then \
        echo "Downloading operon from $OPERON_DOWNLOAD_URL" ; \
        mkdir -p /tmp/operon-export ; \
        curl -fsSL -o /tmp/operon-export/operon "$OPERON_DOWNLOAD_URL" ; \
        chmod +x /tmp/operon-export/operon ; \
    fi

# Allow optional operon binary from build context
COPY operon-linux /build/operon-linux
RUN if [ -f /build/operon-linux ] && [ ! -f /tmp/operon-export/operon ]; then \
        mkdir -p /tmp/operon-export ; \
        cp /build/operon-linux /tmp/operon-export/operon ; \
        chmod +x /tmp/operon-export/operon ; \
    elif [ ! -f /tmp/operon-export/operon ]; then \
        echo "********************************************************************" ; \
        echo "WARNING: operon binary not provided." ; \
        echo "Place operon-linux in the build context, set OPERON_DOWNLOAD_URL," ; \
        echo "or mount the binary at runtime to /root/.local/bin/operon." ; \
        echo "Download from: https://claude.com/product/claude-science" ; \
        echo "********************************************************************" ; \
        mkdir -p /tmp/operon-export ; \
        touch /tmp/operon-export/.placeholder ; \
    fi

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /build/api-bridge && \
    rm -rf /build/api-bridge/.git

FROM ubuntu:${UBUNTU_VERSION}

ENV DEBIAN_FRONTEND=noninteractive
ENV PATH="/root/.local/bin:$PATH"
ENV OPERON_HOME="/root/.claude-science"

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

COPY --from=builder /tmp/operon-export /tmp/operon-import
RUN mkdir -p /root/.local/bin && \
    if [ -f /tmp/operon-import/operon ]; then \
        cp /tmp/operon-import/operon /root/.local/bin/operon && \
        chmod +x /root/.local/bin/operon && \
        echo "operon installed"; \
    else \
        echo "operon binary not provided at build time - mount at runtime"; \
    fi && \
    rm -rf /tmp/operon-import

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
