ARG BASE_IMAGE

FROM $BASE_IMAGE

ARG TARGETPLATFORM
ARG TARGETARCH
ARG GOARCH=${TARGETARCH:-amd64}

RUN apk -U upgrade

# Copy binary from goreleaser context (with $TARGETPLATFORM/) or Makefile context (without)
# The wildcard matches both "sds-linux-arm64" and "linux/arm64/sds-linux-arm64"
# See: https://pkg.go.dev/path/filepath#Match
COPY *sds-linux-$GOARCH /usr/local/bin/sds

USER 10101

ENTRYPOINT ["/usr/local/bin/sds"]
