package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/onsi/gomega"
	"istio.io/istio/pkg/kube/controllers"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/utils/ptr"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// newTestWrapper builds a minimal but valid XdsSnapWrapper for proxyKey with a
// single cluster, so syncXds publishes a non-nil snapshot the cache will store.
func newTestWrapper(proxyKey string) XdsSnapWrapper {
	snapshot := &envoycache.Snapshot{}
	snapshot.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "cluster-a"}},
	})
	return XdsSnapWrapper{
		snap:     snapshot,
		proxyKey: proxyKey,
	}
}

func newTestWatchdog(t *testing.T, ucc ir.UniqlyConnectedClient) (*deferWatchdog, envoycache.SnapshotCache) {
	t.Helper()

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	pt := NewProxyTranslator(cache)
	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})
	wd := newDeferWatchdog(&pt, uccs)
	return wd, cache
}

// TestDeferWatchdogRepublishesStrandedClient covers Case A: a live UCC with no
// snapshot in the cache, a last known wrapper recorded, and an age past the
// threshold. reconcileOnce must republish so GetSnapshot then succeeds.
func TestDeferWatchdogRepublishesStrandedClient(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	key := ucc.ResourceName()

	wd, cache := newTestWatchdog(t, ucc)

	now := time.Unix(1000, 0)
	wd.clock = func() time.Time { return now }

	// Record a last known wrapper as if a snapshot had been built, then the
	// client was deferred (Delete) and never republished.
	wd.observe(krt.Event[XdsSnapWrapper]{
		New:   ptr.To(newTestWrapper(key)),
		Event: controllers.EventAdd,
	})

	// The cache must start empty for this client.
	_, err := cache.GetSnapshot(key)
	g.Expect(err).To(gomega.HaveOccurred(), "cache should have no snapshot for the client initially")

	// Advance the clock past the threshold and reconcile.
	now = now.Add(deferWatchdogThreshold + time.Second)
	wd.reconcileOnce(context.Background(), deferWatchdogThreshold)

	snap, err := cache.GetSnapshot(key)
	g.Expect(err).ToNot(gomega.HaveOccurred(), "watchdog should have republished the last known wrapper")
	g.Expect(snap).ToNot(gomega.BeNil())
}

// TestDeferWatchdogSkipsHealthyClient covers Case B: a live UCC that already
// has a current snapshot in the cache must not be republished.
func TestDeferWatchdogSkipsHealthyClient(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	key := ucc.ResourceName()

	wd, cache := newTestWatchdog(t, ucc)

	now := time.Unix(2000, 0)
	wd.clock = func() time.Time { return now }

	// Publish a healthy snapshot up front.
	healthy := newTestWrapper(key)
	wd.pt.syncXds(context.Background(), healthy)
	before, err := cache.GetSnapshot(key)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Record a different last known wrapper that the watchdog would publish if
	// it (incorrectly) treated the healthy client as stranded.
	other := newTestWrapper(key)
	wd.observe(krt.Event[XdsSnapWrapper]{
		New:   ptr.To(other),
		Event: controllers.EventAdd,
	})

	// Even well past the threshold, a healthy client is skipped.
	now = now.Add(deferWatchdogThreshold + time.Hour)
	wd.reconcileOnce(context.Background(), deferWatchdogThreshold)

	after, err := cache.GetSnapshot(key)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	// The cache must still hold the originally published snapshot, not the
	// watchdog's recorded wrapper.
	g.Expect(after).To(gomega.BeIdenticalTo(before),
		"healthy client snapshot must not be overwritten by the watchdog")
}

// TestDeferWatchdogWaitsUntilThreshold covers Case C: a live UCC with no
// snapshot but whose last change is within the threshold must not be
// republished yet.
func TestDeferWatchdogWaitsUntilThreshold(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	key := ucc.ResourceName()

	wd, cache := newTestWatchdog(t, ucc)

	now := time.Unix(3000, 0)
	wd.clock = func() time.Time { return now }

	// A Delete carries the prior object in Old; Latest() returns it for deletes.
	wd.observe(krt.Event[XdsSnapWrapper]{
		Old:   ptr.To(newTestWrapper(key)),
		Event: controllers.EventDelete,
	})

	// Advance only part way to the threshold.
	now = now.Add(deferWatchdogThreshold / 2)
	wd.reconcileOnce(context.Background(), deferWatchdogThreshold)

	_, err := cache.GetSnapshot(key)
	g.Expect(err).To(gomega.HaveOccurred(), "watchdog must not republish before the threshold elapses")
}
