package gcpprobe

import (
	"context"
	"testing"

	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
)

// Corpus go-control-plane #249 (RF-007): an ADS snapshot with an unrequested
// Secret declines the whole named SDS response. The same superset check is
// already covered for EDS; this pins its Secret resource path as well.
func TestUnrequestedSecretDeclinesSDSResponse(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	newSnapshot := func(version string, secrets ...*envoytlsv3.Secret) *cache.Snapshot {
		t.Helper()
		resources := make([]types.Resource, 0, len(secrets))
		for _, secret := range secrets {
			resources = append(resources, secret)
		}
		snapshot, err := cache.NewSnapshot(version, map[rsrc.Type][]types.Resource{rsrc.SecretType: resources})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return snapshot
	}
	if err := c.SetSnapshot(ctx, kgwNode, newSnapshot("v1", &envoytlsv3.Secret{Name: "a"}, &envoytlsv3.Secret{Name: "b"})); err != nil {
		t.Fatal(err)
	}
	request := &discovery.DiscoveryRequest{TypeUrl: rsrc.SecretType, ResourceNames: []string{"a"}}
	subscription := stream.NewSotwSubscription([]string{"a"}, false)
	ch := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(request, subscription, ch); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch, "the unrequested Secret b declines the named SDS response for a")

	if err := c.SetSnapshot(ctx, kgwNode, newSnapshot("v2", &envoytlsv3.Secret{Name: "a"})); err != nil {
		t.Fatal(err)
	}
	filtered := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(request, subscription, filtered); err != nil {
		t.Fatal(err)
	}
	response := expectResponse(t, filtered, "filtered SDS snapshot answers the named request")
	if got := response.GetReturnedResources(); len(got) != 1 || got["a"] == "" {
		t.Fatalf("returned resources %v, want only a", got)
	}
}
