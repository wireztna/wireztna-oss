FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    wireguard-tools \
    iproute2 \
    nftables \
    iptables \
    dnsmasq \
    jq \
    curl \
    bash \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir httpx

# Copy scripts
COPY scripts/ /opt/wireztna/scripts/
RUN chmod +x /opt/wireztna/scripts/*.sh

# Copy agent
COPY agent/ /opt/wireztna/agent/

WORKDIR /opt/wireztna

CMD ["python", "-m", "agent.reconciler"]
