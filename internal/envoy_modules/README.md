# Envoy Dynamic Module

This module was initially based on https://github.com/envoyproxy/dynamic-modules-examples

It implements kgateway features as Envoy dynamic modules compiled into a single `librust_module.so`.

## Project Organization

This module is organized as [Rust Workspaces](https://doc.rust-lang.org/cargo/reference/workspaces.html) with the following crates:

- `module-init`: The cdylib entry point. Registers Envoy init hooks via `declare_init_functions!`
  and dispatches incoming filter names to the appropriate module crate. The `[lib] name = "rust_module"`
  setting controls the output `.so` filename.
- `filters/rustformation`: Envoy HTTP filter adapter for the `rustformation` filter. Contains all
  Envoy SDK adapter code and bridges to `lib/jina` for the transformation logic.
- `filters/kgateway-example-filter`: Reference skeleton showing the minimum code required to implement
  a new filter. Not registered in `module-init`; does not affect the built `.so`.
- `lib/transformation`: library with transformation abstraction and Jinja template rendering (no Envoy dependencies). Provides the template
  rendering logic shared by modules.

To add a new module, see [docs/adding-a-module.md](docs/adding-a-module.md).

## Building

The Dockerfile that builds the envoy wrapper image is in `/cmd/envoyinit/Dockerfile`. It pulls in
the envoy binary, this dynamic module, and the envoyinit binary into the image.

To build the envoy wrapper docker image, at the kgateway top project level, run:

```bash
make envoy-wrapper-docker
```

A custom `ENVOY_IMAGE` can be used, but make sure the Rust SDK version is compatible:

```bash
ENVOY_IMAGE=<custom envoy image> make envoy-wrapper-docker
```

## Formatting and Linting

Before creating a PR, run:

```bash
make lint
```

## Testing

### Unit testing

```bash
cargo test --workspace
```

### E2E testing

At the kgateway project top level directory, run:

```bash
hack/run-e2e-test.sh TestKgateway/^Transforms$/TestGatewayWithTransformation
```

## Envoy upgrade

The Envoy SDK tag in the Cargo dependencies must match the Envoy version being used.
See [envoy-upgrade](../../devel/envoy/envoy-upgrade.md) for details.
