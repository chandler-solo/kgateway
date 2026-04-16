ARG ENVOY_IMAGE
ARG CARGO_ZBUILD_IMAGE=ghcr.io/rust-cross/cargo-zigbuild:0.19.8
# Stage 1: pull in all rust build dependency and cache it in docker build layer cache
FROM --platform=$TARGETPLATFORM ${CARGO_ZBUILD_IMAGE} AS rust_build_deps
WORKDIR /build
RUN apt update && apt install -y clang
ARG ENVOY_MODULES_DIR=./envoy_modules
# Unfortunately, there is no easy way to find all the Cargo.toml files and iterate
# through the directories in Dockerfile. If we add more local crates, add their
# Cargo.toml files below. See /docs/guides/adding-a-filter.md.
COPY ${ENVOY_MODULES_DIR}/Cargo.toml ${ENVOY_MODULES_DIR}/Cargo.lock ./
RUN mkdir -p module-init/src \
              filters/rustformation/src \
              lib/transformation/src \
              lib/envoy-helpers/src \
    && echo "pub fn dummy() {}" > module-init/src/lib.rs \
    && echo "pub fn dummy() {}" > filters/rustformation/src/lib.rs \
    && echo "pub fn dummy() {}" > lib/transformation/src/lib.rs \
    && echo "pub fn dummy() {}" > lib/envoy-helpers/src/lib.rs
COPY ${ENVOY_MODULES_DIR}/module-init/Cargo.toml ./module-init
COPY ${ENVOY_MODULES_DIR}/filters/rustformation/Cargo.toml ./filters/rustformation
COPY ${ENVOY_MODULES_DIR}/lib/transformation/Cargo.toml ./lib/transformation
COPY ${ENVOY_MODULES_DIR}/lib/envoy-helpers/Cargo.toml ./lib/envoy-helpers
ARG RUST_BUILD_ARCH=x86_64 # options are x86_64 or aarch64
# Because we built the dummy lib in this Stage to cache all the dependencies in docker layer cache
# That unfortunately also cached the dummy libs, and in Stage 2 when we COPY, it preserved
# the timestamps of the source code, so it never re-compile the real lib. Need to remove them
# here
RUN cargo fetch \
    && cargo zigbuild --target ${RUST_BUILD_ARCH}-unknown-linux-gnu \
    && find /build/target \( -name librust_module.so \
        -o -name 'libtransformation*.rlib' \
        -o -name 'libenvoy_helpers*.rlib' \
        -o -name 'librustformation_filter*.rlib' \
    \) -type f -delete

# Stage 2: build envoy dynamic module
FROM --platform=$TARGETPLATFORM ${CARGO_ZBUILD_IMAGE} AS rust_builder
WORKDIR /build
RUN apt update && apt install -y clang

# Copy cached target and cargo dirs from rust_build_deps from step 1
COPY --from=rust_build_deps /usr/local/cargo /usr/local/cargo
COPY --from=rust_build_deps /build/target /build/target

# Copy the source code and build the module.
ARG ENVOY_MODULES_DIR=./envoy_modules
COPY ${ENVOY_MODULES_DIR} .
ARG RUST_BUILD_ARCH=x86_64 # set this to "aarch64" for local arm build
RUN cargo zigbuild --target ${RUST_BUILD_ARCH}-unknown-linux-gnu

RUN cp /build/target/${RUST_BUILD_ARCH}-unknown-linux-gnu/debug/librust_module.so /build/librust_module.so

# Stage 3: build the final envoy wrapper image
FROM --platform=$TARGETPLATFORM ${ENVOY_IMAGE} AS envoy
ENV DEBIAN_FRONTEND=noninteractive

# Update our deps to make cve toil lower
# install wget for our default probes
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install wget ca-certificates -y \
    && rm -rf  /var/log/*log /var/lib/apt/lists/* /var/log/apt/* /var/lib/dpkg/*-old /var/cache/debconf/*-old

ARG GOARCH=amd64
COPY envoyinit-linux-$GOARCH /usr/local/bin/envoyinit

ENV ENVOY_DYNAMIC_MODULES_SEARCH_PATH=/usr/local/lib
COPY --from=rust_builder /build/librust_module.so /usr/local/lib/librust_module.so

# SDS-specific setup, only used if ENVOY_SIDECAR=true
ARG ENTRYPOINT_SCRIPT=/docker-entrypoint.sh
COPY $ENTRYPOINT_SCRIPT /

USER 10101

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD []
