# 单一 Go 二进制：Anthropic→OpenAI 桥、forward/local CLI、setup-token 都在里面。
# 桥的翻译逻辑仍是标准库；SSH 转发使用 golang.org/x/crypto/ssh。
# 代码与 pixi 的 build / test 任务共用同一份源码。
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY config/ ./config/
COPY forward/ ./forward/
COPY local/ ./local/
COPY proxy/ ./proxy/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/claude-science-container .

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV PYTHONUNBUFFERED=1
ENV LANG=zh_CN.UTF-8
ENV LANGUAGE=zh_CN:zh
ENV LC_ALL=zh_CN.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bubblewrap \
    socat \
    curl \
    git \
    gh \
    p7zip \
    python3 \
    sqlite3 \
    supervisor \
    locales \
    fonts-noto-cjk \
    && locale-gen zh_CN.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt

# 一个静态二进制同时提供 serve / setup-token（容器链路）与 forward / local（桌面）。
COPY --from=build /out/claude-science-container /usr/local/bin/claude-science-container
RUN chmod 755 /usr/local/bin/claude-science-container

ARG CACHE_BUST

RUN CACHE_BUST=${CACHE_BUST} curl -fsSL --connect-timeout 10 --max-time 300 --retry 3 --retry-delay 5 \
        -o /usr/local/bin/claude-science "https://downloads.claude.ai/claude-science/latest/linux-x64" && \
    chmod +x /usr/local/bin/claude-science

COPY scripts/supervisord.conf /etc/supervisor/conf.d/claude-science.conf
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY scripts/configure-cjk-fonts.py /usr/local/bin/configure-cjk-fonts.py
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 9981

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
