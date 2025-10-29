ARG ENVOY_IMAGE

FROM ghcr.io/rust-cross/cargo-zigbuild:0.19.88@sha256:a00024c136a365171892a186b1ee5552812a8f91f2ba9f5fe609c0d352238cca AS deps

WORKDIR /build

RUN apt update && apt install -y clang

# Paramerized to allow for supporting local, make and goreleaser builds
ARG RUSTFORMATIONS_DIR=./rustformations

COPY ${RUSTFORMATIONS_DIR}/Cargo.toml ${RUSTFORMATIONS_DIR}/Cargo.lock ./

RUN mkdir src && echo "" > src/lib.rs

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    cargo fetch

FROM deps AS builder

RUN rm -rf src

# Copy the actual source code - this changes frequently but deps are cached
COPY ${RUSTFORMATIONS_DIR} .

# Build with cache mount for incremental compilation.
# This speeds up rebuilds when only source files change.
# Hard code amd64 for now as we only have that version of envoy-gloo.
RUN --mount=type=cache,target=/build/target \
    --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    cargo zigbuild --target x86_64-unknown-linux-gnu && \
    cp /build/target/x86_64-unknown-linux-gnu/debug/librust_module.so /build/amd64_librust_module.so
# switch to this locally if you are on arm
# RUN cargo zigbuild --target aarch64-unknown-linux-gnu

FROM $ENVOY_IMAGE AS envoy
# hard code amd64 for now as we only have that version of envoy-gloo
ARG TARGETPLATFORM
ARG TARGETARCH
ARG GOARCH=${TARGETARCH:-amd64}
# eventually may matter for now https://unix.stackexchange.com/a/701288
# means its not too useful
ENV DEBIAN_FRONTEND=noninteractive

# Update our deps to make cve toil lower
#install wget for our default probes
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install wget ca-certificates -y \
    && rm -rf  /var/log/*log /var/lib/apt/lists/* /var/log/apt/* /var/lib/dpkg/*-old /var/cache/debconf/*-old

# Copy binary from goreleaser context (with $TARGETPLATFORM/) or Makefile context (without)
# The wildcard matches both "envoyinit-linux-arm64" and "linux/arm64/envoyinit-linux-arm64"
# See: https://pkg.go.dev/path/filepath#Match
COPY *envoyinit-linux-$GOARCH /usr/local/bin/envoyinit

ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/usr/local/lib
COPY --from=builder /build/amd64_librust_module.so /usr/local/lib/librust_module.so


# SDS-specific setup, only used if ENVOY_SIDECAR=true
ARG ENTRYPOINT_SCRIPT=/docker-entrypoint.sh
COPY $ENTRYPOINT_SCRIPT /

USER 10101

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD []
