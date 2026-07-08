package proxy_syncer

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
)

// assertSharedProtos guards the core invariant of the base+overlay split: the
// Cluster and ClusterLoadAssignment protos interned and shared across UCC
// snapshots are read-only after creation. A later mutation corrupts every
// client sharing the proto AND the copy stored in KRT — invisibly, because the
// version hashes were computed at store time, so KRT equality can never see
// the drift and the corruption persists until the backend legitimately
// changes.
//
// When enabled, the producing collections capture each shared proto's content
// hash at creation, and snapshotPerClient re-hashes at assembly time and
// panics on drift, naming the offending resource. Off by default in
// production: the re-hash is a full deterministic marshal per resource per
// snapshot rebuild, which is exactly the cost the interning exists to avoid.
//
// Enabled in CI two ways: this package's tests force it on in-process (see
// TestMain), and the e2e suites set it on the deployed controller via
// controller.extraEnv in test/e2e/tests/manifests/common-recommendations.yaml,
// so real xDS serving with live Envoy clients runs with the tripwire armed. A
// trip in e2e surfaces as a controller panic/restart; the message is in the
// previous container's logs (kubectl logs --previous).
var assertSharedProtos = envutils.IsEnvTruthy("ASSERT_SHARED_PROTO_IMMUTABILITY")

// captureSharedProtoHash records the content hash of a shared proto at
// creation time. Returns 0 when assertions are disabled; 0 means "not
// captured" and disables verification for that resource (test fixtures that
// construct rows directly get this for free).
func captureSharedProtoHash(msg proto.Message) uint64 {
	if !assertSharedProtos {
		return 0
	}
	return utils.HashProto(msg)
}

// sharedProtoAssertValue passes through an already-computed content hash as
// the capture value when assertions are enabled. Used where the producer has
// just computed utils.HashProto for versioning anyway, so capture is free.
func sharedProtoAssertValue(protoHash uint64) uint64 {
	if !assertSharedProtos {
		return 0
	}
	return protoHash
}

// verifySharedProtoHash re-hashes a shared proto at snapshot-assembly time and
// panics if it no longer matches its creation-time hash. captured == 0 means
// the resource was not captured (assertions were off at creation, or the row
// came from a test fixture) and is skipped.
func verifySharedProtoHash(kind, name, uccName string, msg proto.Message, captured uint64) {
	if captured == 0 {
		return
	}
	if got := utils.HashProto(msg); got != captured {
		panic(fmt.Sprintf(
			"shared %s proto %q was mutated after creation (hash %d at creation, %d now), detected assembling the snapshot for %s: "+
				"protos returned by the per-client cluster/endpoint collections are shared across clients and MUST NOT be mutated — clone before modifying",
			kind, name, captured, got, uccName))
	}
}
