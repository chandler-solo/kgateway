# Direct runtime profiles

`profiles.json` pins upstream v1.39.1 by digest. It names four scenarios and every required
observation phase. Run all profiles with:

```sh
make formal-runtime-profiles
# Select the public upstream profile explicitly.
make formal-runtime-profiles FORMAL_RUNTIME_ARGS='--profile upstream'
```

The runner builds the existing probe through a Make target, pulls exact digests,
and records image IDs, resolved architecture, source/binary hashes, phase
receipts and hashes of config dumps, bootstrap, logs and observations. Missing
images, failed scenarios, missing/duplicated required phases and reused artifact
directories fail. Success is written only after every selected scenario passes.

The October 16 local run passed all four upstream scenarios on arm64. Observations
come from actual Envoy admin state, HTTP requests and TLS handshakes: initialized
empty assignments can serve 503; unchanged-version cache republication does not
force EDS delivery; CDS/LDS can apply valid siblings while NACKing; ADS secret
rotation/removal and restart behavior match the existing scenario assertions.
The restart profile enables `use_eds_cache_for_ads` and leaves the fetch timeout
unset, preserving RF-028's default 15-second fallback correction. The separate
warming-cache profile explicitly disables fetch timeout and observes only a
finite silence window before supplying a new EDS revision.

These runs use the research probe, not a kgateway controller or the proposed
stack. They cover one worker, selected standard Envoy resources and ADS secret
fixtures. They do not validate authenticated HA/shared-key publication, standalone
SDS, arbitrary secret types, multi-worker activation or a deployed controller.
