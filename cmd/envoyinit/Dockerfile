# syntax=docker/dockerfile:1.7
ARG ENVOY_IMAGE
ARG CARGO_ZBUILD_IMAGE=ghcr.io/rust-cross/cargo-zigbuild:0.19.8
# Stage 1: pull in all rust build dependency and cache it in docker build layer cache
FROM --platform=$TARGETPLATFORM ${CARGO_ZBUILD_IMAGE} AS rust_build_deps
WORKDIR /build
RUN sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list 2>/dev/null || true \
    && sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true \
    && apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install --no-install-recommends -y clang
ARG ENVOY_MODULES_DIR=./envoy_modules
# Discover workspace manifests from the build context so new filters do not require
# Dockerfile edits, while still keeping dependency caching separate from copying full sources.
RUN --mount=type=bind,target=/context,ro \
    set -eux; \
    modules_dir="/context/${ENVOY_MODULES_DIR#./}"; \
    cp "${modules_dir}/Cargo.toml" "${modules_dir}/Cargo.lock" ./; \
    find "${modules_dir}" \
        \( -type d \( -name target -o -name pkg -o -name vendor \) -prune \) -o \
        -type f -name Cargo.toml -print \
    | while read -r manifest; do \
        rel_dir="$(dirname "${manifest#"${modules_dir}/"}")"; \
        if [ "${rel_dir}" = "." ]; then \
            continue; \
        fi; \
        mkdir -p "./${rel_dir}/src"; \
        cp "${manifest}" "./${rel_dir}/Cargo.toml"; \
        printf 'pub fn dummy() {}\n' > "./${rel_dir}/src/lib.rs"; \
    done
ARG RUST_BUILD_ARCH=x86_64 # options are x86_64 or aarch64
# Because we built the dummy lib in this Stage to cache all the dependencies in docker build layer cache,
# the dummy libs are also cached. In Stage 2 when we COPY, it preserves the timestamps of the
# source code, so it never re-compiles the real lib.
RUN --mount=type=bind,target=/context,ro \
    set -eux; \
    modules_dir="/context/${ENVOY_MODULES_DIR#./}"; \
    artifacts="/tmp/envoy-module-artifacts"; \
    : > "${artifacts}"; \
    find "${modules_dir}" \
        \( -type d \( -name target -o -name pkg -o -name vendor \) -prune \) -o \
        -type f -name Cargo.toml -print | sort \
    | while read -r manifest; do \
        rel_dir="$(dirname "${manifest#"${modules_dir}/"}")"; \
        if [ "${rel_dir}" = "." ]; then \
            continue; \
        fi; \
        crate_name="$(awk ' \
            /^\[lib\]/     { in_lib=1; in_pkg=0; next } \
            /^\[package\]/ { in_pkg=1; in_lib=0; next } \
            /^\[/          { in_lib=0; in_pkg=0; next } \
            in_lib && /^[[:space:]]*name[[:space:]]*=/ && lib_name == "" { \
                sub(/^[[:space:]]*name[[:space:]]*=[[:space:]]*"/, "", $0); \
                sub(/".*$/, "", $0); \
                lib_name = $0; \
                next; \
            } \
            in_pkg && /^[[:space:]]*name[[:space:]]*=/ && pkg_name == "" { \
                sub(/^[[:space:]]*name[[:space:]]*=[[:space:]]*"/, "", $0); \
                sub(/".*$/, "", $0); \
                pkg_name = $0; \
            } \
            END { print (lib_name != "" ? lib_name : pkg_name) } \
        ' "${manifest}")"; \
        crate_name="$(printf '%s' "${crate_name}" | tr '-' '_')"; \
        printf '%s\n' "lib${crate_name}-*" >> "${artifacts}"; \
        if awk ' \
            /^\[lib\]/ { in_lib=1; next } \
            /^\[/      { in_lib=0 } \
            in_lib && /crate-type[[:space:]]*=/ && /cdylib/ { found=1 } \
            END { exit(found ? 0 : 1) } \
        ' "${manifest}"; then \
            printf '%s\n' "lib${crate_name}.so" >> "${artifacts}"; \
        fi; \
    done; \
    cargo fetch; \
    cargo zigbuild --target ${RUST_BUILD_ARCH}-unknown-linux-gnu; \
    sort -u "${artifacts}" | while read -r artifact; do \
        find /build/target -type f -name "${artifact}" -delete; \
    done; \
    rm -f "${artifacts}"

# Stage 2: build envoy dynamic module
FROM --platform=$TARGETPLATFORM ${CARGO_ZBUILD_IMAGE} AS rust_builder
WORKDIR /build
RUN sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list 2>/dev/null || true \
    && sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true \
    && apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install --no-install-recommends -y clang

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
RUN (sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list 2>/dev/null || true) \
    && (sed -i 's|http://archive.ubuntu.com|http://azure.archive.ubuntu.com|g; s|http://security.ubuntu.com|http://azure.archive.ubuntu.com|g' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true) \
    && apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 upgrade -y \
    && apt-get -o Acquire::Retries=5 install --no-install-recommends -y ca-certificates wget \
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
