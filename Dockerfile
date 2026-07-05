FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV PYTHONUNBUFFERED=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bubblewrap \
    socat \
    curl \
    git \
    python3 \
    python3-venv \
    python3-pip \
    supervisor \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt

RUN git clone --depth 1 https://github.com/Jyx0208/claude-science-api-bridge.git /opt/api-bridge && \
    rm -rf /opt/api-bridge/.git

RUN python3 -m venv /opt/api-bridge/.venv && \
    /opt/api-bridge/.venv/bin/pip3 install --no-cache-dir --upgrade pip && \
    /opt/api-bridge/.venv/bin/pip3 install --no-cache-dir -r /opt/api-bridge/requirements.txt && \
    cp /opt/api-bridge/config.example.json /opt/api-bridge/config.json && \
    chmod 600 /opt/api-bridge/config.json && \
    python3 -c "
path = '/opt/api-bridge/proxy.py'
with open(path) as f:
    lines = f.readlines()
new_lines = []
for line in lines:
    new_lines.append(line)
    if 'if \"max_tokens\" in out:' in line:
        new_lines.append('    if \"thinking\" in out:\n')
        new_lines.append('        del out[\"thinking\"]\n')
with open(path, 'w') as f:
    f.writelines(new_lines)
"

RUN curl -fsSL --connect-timeout 10 --max-time 60 \
        -o /usr/local/bin/claude-science "https://downloads.claude.ai/claude-science/latest/linux-x64" && \
    chmod +x /usr/local/bin/claude-science

COPY scripts/supervisord.conf /etc/supervisor/conf.d/claude-science.conf
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 9876 9981

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
