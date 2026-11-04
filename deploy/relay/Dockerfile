# syntax=docker/dockerfile:1
# Build context: repository root.
#   docker build -f deploy/relay/Dockerfile -t lanfolk-relay:latest .

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal ./internal
COPY cmd/lanfolk-relay ./cmd/lanfolk-relay
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X github.com/NotrinoResearch/lanfolk/internal/version.Version=${VERSION}" \
      -o /out/lanfolk-relay ./cmd/lanfolk-relay
# Allow the non-root user to bind TCP 443 without running as root.
RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin \
    && setcap cap_net_bind_service=+ep /out/lanfolk-relay \
    && getcap /out/lanfolk-relay

FROM scratch
LABEL org.opencontainers.image.title="lanfolk-relay" \
      org.opencontainers.image.description="Lanfolk shared relay: STUN, signaling and fallback forwarding" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/lanfolk-relay /lanfolk-relay
USER 65532:65532
ENTRYPOINT ["/lanfolk-relay"]
CMD ["serve"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/lanfolk-relay", "healthcheck"]
