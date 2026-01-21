ARG ENVOY_IMAGE
ARG BASE_IMAGE=debian:bookworm-slim

# Stage 1: Source for envoy binary
FROM --platform=$TARGETPLATFORM ${ENVOY_IMAGE} AS envoy-bin

# Stage 2: Final envoy wrapper image with pre-built rustformations module
FROM --platform=$TARGETPLATFORM ${BASE_IMAGE}
ENV DEBIAN_FRONTEND=noninteractive

# Install minimal runtime dependencies and apply security updates
# wget is used for default probes, ca-certificates for TLS
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends wget ca-certificates \
    && rm -rf /var/lib/apt/lists/* /var/log/*log /var/log/apt/* /var/lib/dpkg/*-old /var/cache/debconf/*-old

# Copy envoy binary from the envoy image
COPY --from=envoy-bin /usr/local/bin/envoy /usr/local/bin/envoy

ARG GOARCH=amd64
COPY envoyinit-linux-$GOARCH /usr/local/bin/envoyinit

# Copy pre-built rustformations dynamic module
ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/usr/local/lib
ARG RUST_LIB_PATH
COPY ${RUST_LIB_PATH} /usr/local/lib/librust_module.so

# SDS-specific setup, only used if ENVOY_SIDECAR=true
ARG ENTRYPOINT_SCRIPT=/docker-entrypoint.sh
COPY $ENTRYPOINT_SCRIPT /

USER 10101

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD []
