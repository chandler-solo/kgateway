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

// RF-020, RF-023: the standalone SDS server registers all three RPC modes.
// This characterizes StreamSecrets and DeltaSecrets with the production
// registration and a synthetic public secret: both serve the configured
// client's snapshot to any Node identity, like FetchSecrets. It records the
// reachable surface; it does not decide which modes kgateway supports.
func TestSDSStreamAndDeltaServeAnyNodeIdentity(t *testing.T) {
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

	for _, id := range []string{"configured-client", "different-client"} {
		t.Run("stream/node="+id, func(t *testing.T) {
			stream, err := client.StreamSecrets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&discovery.DiscoveryRequest{Node: &envoycorev3.Node{Id: id}, TypeUrl: resource.SecretType, ResourceNames: []string{"synthetic-public-fixture"}}); err != nil {
				t.Fatal(err)
			}
			resp, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetVersionInfo() != "v1" || len(resp.GetResources()) != 1 || resp.GetNonce() == "" {
				t.Fatalf("unexpected stream response: %v", resp)
			}
			_ = stream.CloseSend()
		})
		t.Run("delta/node="+id, func(t *testing.T) {
			stream, err := client.DeltaSecrets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&discovery.DeltaDiscoveryRequest{Node: &envoycorev3.Node{Id: id}, TypeUrl: resource.SecretType, ResourceNamesSubscribe: []string{"synthetic-public-fixture"}}); err != nil {
				t.Fatal(err)
			}
			resp, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.GetResources()) != 1 || resp.GetResources()[0].GetName() != "synthetic-public-fixture" || resp.GetNonce() == "" {
				t.Fatalf("unexpected delta response: %v", resp)
			}
			_ = stream.CloseSend()
		})
	}
}
