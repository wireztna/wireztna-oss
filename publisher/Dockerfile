FROM alpine:3.19

RUN apk add --no-cache \
    wireguard-tools \
    iptables \
    curl \
    jq \
    bash \
    dnsmasq \
    && echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

COPY scripts/ /opt/wireztna/
RUN chmod +x /opt/wireztna/*.sh

# Version file — read by heartbeat.sh if AGENT_VERSION env not set
ARG VERSION=0.4.0
RUN echo "$VERSION" > /etc/wireztna-version

ENV CONFIG_DIR=/etc/wireguard
ENV WG_INTERFACE=wg-broker
ENV HEARTBEAT_INTERVAL=30

ENTRYPOINT ["/opt/wireztna/entrypoint.sh"]

# NOTE: For DNS auto-detection, mount host resolv.conf:
#   -v /etc/resolv.conf:/host/etc/resolv.conf:ro
