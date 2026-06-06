# Kgateway releases

## Rolling `main` builds

Automation is in place to build and publish releases for all commits merged into the `main` branch.

This enables devs and users to have concrete artifacts for testing which contain features and bug fixes which have not yet made it into a patch or minor release.

The version is rolling, based on the next minor version release, e.g. `v2.4.0-main`.

The usable artifacts are pushed to GHCR and visible on the [packages page](https://github.com/orgs/kgateway-dev/packages?repo_name=kgateway).

Typically this will be consumed via the helm charts, and can be used directly, such as:
```bash
helm install kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds --version v2.4.0-main --namespace kgateway-system --create-namespace
helm install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --version v2.4.0-main --namespace kgateway-system --create-namespace
```

## Verifying signatures

Release artifacts are signed keylessly with cosign by the release workflow. Use the workflow identity and issuer below when verifying official kgateway artifacts:

```bash
COSIGN_CERT_IDENTITY='^https://github.com/kgateway-dev/kgateway/.github/workflows/release.yaml@refs/heads/(main|v[0-9]+\.[0-9]+\.x)$'
COSIGN_CERT_ISSUER='https://token.actions.githubusercontent.com'
```

Verify container images:

```bash
VERSION=v2.4.0
cosign verify --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" cr.kgateway.dev/kgateway-dev/kgateway:${VERSION}
cosign verify --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" cr.kgateway.dev/kgateway-dev/sds:${VERSION}
cosign verify --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" cr.kgateway.dev/kgateway-dev/envoy-wrapper:${VERSION}
```

Verify Helm charts:

```bash
VERSION=v2.4.0
cosign verify --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" cr.kgateway.dev/kgateway-dev/charts/kgateway:${VERSION}
cosign verify --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" cr.kgateway.dev/kgateway-dev/charts/kgateway-crds:${VERSION}
```

To verify GitHub release assets, download the checksum file and its `.sigstore.json` bundle from the release page, then run:

```bash
cosign verify-blob --certificate-identity-regexp "$COSIGN_CERT_IDENTITY" --certificate-oidc-issuer "$COSIGN_CERT_ISSUER" --bundle kgateway_*_checksums.txt.sigstore.json kgateway_*_checksums.txt
```

## Developer documentation

Please refer to [devel/contributing/releasing.md](devel/contributing/releasing.md).
