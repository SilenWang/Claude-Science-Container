# /opt/api-bridge 的 Go 实现：只用标准库，构建阶段产出静态二进制。
# 代码与 pixi 的 build_bridge / test_bridge 任务共用同一份 proxy/ 源码。
FROM golang:1.26 AS bridge-build
WORKDIR /src
COPY proxy/ ./proxy/
RUN cd proxy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api-bridge .

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

# 替换上游 Jyx0208/claude-science-api-bridge（Python）：不再 clone、打 patch、
# 建 venv 或安装 pip 依赖，只拷一个静态二进制。
COPY --from=bridge-build /out/api-bridge /opt/api-bridge/api-bridge
RUN chmod 755 /opt/api-bridge/api-bridge
# 运行层不应再有上游 Python 桥接的虚拟环境；构建期直接断言。
RUN test ! -e /opt/api-bridge/.venv

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
