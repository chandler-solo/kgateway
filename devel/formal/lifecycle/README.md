# Cache and server lifecycle observations

`make formal-lean` now emits and replays four fresh traces from the real
go-control-plane dependency in the root `go.mod`. These complement the existing
Lean snapshot traces; they are not a joined controller-to-Envoy replay.

| Probe | Observed boundary | Model connection |
|---|---|---|
| Immediately declined named request | No registered watch; a later eligible publication produces no response in the synchronous probe | `WatchDeliveryCompositionImmediateLost`: temporal negative control with no new client request |
| Parked named request declined during publication | The watch remains; a later eligible snapshot answers without another request | Admission retention precondition in `WatchDeliveryCompositionFixed` |
| Canceled partial fan-out | CDS response observed, EDS watch retained, `SetSnapshot` returns cancellation, both new cache versions read back | Separate installation, failure and response transitions; `NoRetry` preserves the missing retry assumption |
| Server request/NACK/ACK | Actual server callbacks and responses received by a mock transport, correlated by stream, type, request and nonce | ACK and application remain independent; no application event is emitted |

The cache API profile uses a logical observation scope, not a gRPC stream. Its
watch counts are meaningful because each scenario controls its outstanding
watches. The server profile uses real server stream IDs and callbacks, with a
scripted in-process client. `response-attempt` is the pre-send callback;
`transport-observed` means that mock received the response. Neither is a claim
that a real Envoy received or applied it.

`cache-install` is a successful `GetSnapshot` readback. It may follow callbacks
and a failed `SetSnapshot`, so replay requires a matching readback but does not
invent a causal ordering from its observation time. `publication-result` records
the API result separately. These events do not instrument the internal enqueue
operation. No secret contents or configuration payloads are recorded.

Schema 1 requires contiguous sequence numbers, a stable scenario/profile,
open stream/key ownership, request/response correlation and an exact terminal
event/count receipt. Unknown JSON fields/events, unmatched ACKs, wrong nonces,
wrong versions, missing suffixes, duplicate terminals and reused stream IDs
fail. An attempted application event also fails: that observation needs its own
independent instrumentation. Terminal counts cover emitted events only.

The Go checker and mutation tests are required by the execution-receipt gate.
The CLI `devel/formal/cmd/checklifecycle` replays saved JSONL files. Set
`FORMAL_LIFECYCLE_OUT` to a fresh directory when running the Go suites through
Make; files are created exclusively, so a prior trace cannot substitute for a
fresh run. The Lean runner requires exactly the four expected trace files.

Remaining work: join these records to publication decisions and all per-type
versions, include callback/timer and subscription generations, observe internal
queues/cancellation, audit instrumentation coverage, and correlate independent
Envoy admin/traffic observations. The new generation model is a proposed policy,
not proof that this trace format or the stack enforces it.
