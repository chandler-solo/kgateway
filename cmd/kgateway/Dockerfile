ARG BASE_IMAGE=debian:bookworm-slim

FROM --platform=$TARGETPLATFORM ${BASE_IMAGE}
ENV DEBIAN_FRONTEND=noninteractive

# Install minimal runtime dependencies and apply security updates
# wget is used for default probes, ca-certificates for TLS
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends wget ca-certificates \
    && rm -rf /var/lib/apt/lists/* /var/log/*log /var/log/apt/* /var/lib/dpkg/*-old /var/cache/debconf/*-old

ARG GOARCH=amd64
COPY kgateway-linux-$GOARCH /usr/local/bin/kgateway
COPY envoyinit-linux-$GOARCH /usr/local/bin/envoyinit

# Copy pre-built rustformations dynamic module
ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/usr/local/lib
ARG RUST_LIB_PATH
COPY ${RUST_LIB_PATH} /usr/local/lib/librust_module.so

USER 10101

ENTRYPOINT ["/usr/local/bin/kgateway"]
