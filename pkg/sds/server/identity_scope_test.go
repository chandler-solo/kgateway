package server

import (
	"context"
	"net"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	secretservice "github.com/envoyproxy/go-control-plane/envoy/service/secret/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// RF-023: the configured SDS client is a cache key, not an authenticated
// identity. Use synthetic public data and the production registration/options.
// This characterizes Fetch; streaming and deployment reachability remain open.
func TestSDSFetchDoesNotAuthorizeNodeIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := SetupEnvoySDS(ctx, nil, "configured-client", "127.0.0.1:0")
	snapshot, err := cache.NewSnapshot("v1", map[resource.Type][]types.Resource{
		resource.SecretType: {&envoytlsv3.Secret{Name: "synthetic-public-fixture"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.snapshotCache.SetSnapshot(ctx, "configured-client", snapshot); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1024 * 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.grpcServer.Serve(lis)
	}()
	t.Cleanup(func() {
		s.grpcServer.Stop()
		_ = lis.Close()
		<-done
	})
	conn, err := grpc.NewClient("passthrough:///sds-probe",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := secretservice.NewSecretDiscoveryServiceClient(conn)
	for _, id := range []string{"", "configured-client", "different-client"} {
		t.Run("node="+id, func(t *testing.T) {
			var node *envoycorev3.Node
			if id != "" {
				node = &envoycorev3.Node{Id: id}
			}
			resp, err := client.FetchSecrets(ctx, &discovery.DiscoveryRequest{Node: node})
			if err != nil {
				t.Fatal(err)
			}
			if resp.VersionInfo != "v1" || len(resp.Resources) != 1 {
				t.Fatal("unexpected synthetic snapshot response")
			}
			var got envoytlsv3.Secret
			if err := resp.Resources[0].UnmarshalTo(&got); err != nil {
				t.Fatal(err)
			}
			if got.Name != "synthetic-public-fixture" {
				t.Fatal("unexpected resource identity")
			}
		})
	}
}
