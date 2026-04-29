# verify-helm-schema

Verifies that `install/helm/kgateway/values.yaml` and
`install/helm/kgateway/values.schema.json` stay in sync. Walks both trees in
parallel and fails if any key is present in one but missing from the other.
Checks top-level and nested objects (e.g. `controller.image`,
`controller.service`). It only recurses into a nested object when both sides
agree it is an object, so user-data keys (annotation values, label maps) and
schema-only sub-properties under `{}` defaults do not produce false positives.

## Run

From the repository root:

```
make verify-helm-schema
```

Or directly:

```
go -C hack/utils/verify-helm-schema run . \
  -values install/helm/kgateway/values.yaml \
  -schema install/helm/kgateway/values.schema.json
```

Exit code is non-zero on drift, zero when both files agree.
