# Test image for the Lanfolk NAT simulation (test/natsim/run.sh).
# Build the Linux binaries on the host first (test/natsim/run-local.sh does it):
#   CGO_ENABLED=0 GOOS=linux go build -o test/natsim/bin/lanfolk-relay ./cmd/lanfolk-relay
# The build context is test/natsim.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates conntrack iproute2 iputils-ping jq kmod nftables procps tcpdump \
 && rm -rf /var/lib/apt/lists/*

COPY bin/lanfolk-relay bin/lanfolk-service bin/lanfolk-cli /usr/local/bin/
COPY run.sh /natsim/run.sh
RUN chmod 0755 /usr/local/bin/lanfolk-* /natsim/run.sh

# Needs --privileged (network namespaces, nftables, TUN devices).
WORKDIR /natsim
ENTRYPOINT ["/natsim/run.sh"]
