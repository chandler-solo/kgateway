//go:build formal_oracle

package proxy_syncer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/proxy_syncer/sharedproto"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// A separate adapter exercises snapshotPerClient itself. Each frozen candidate
// is read through real KRT joins. The fast fuzzer controls publication schedules
// without depending on sleeps or assuming KRT executes every intermediate row.
func oracleBuildKRT(t *testing.T, input oracleInput, client string) *XdsSnapWrapper {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opts := krtutil.NewKrtOptions(ctx.Done(), nil)
	ucc := oracleUCC(client)
	uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})
	cs := []uccWithCluster{}
	for _, c := range input.Clusters {
		h, _ := strconv.ParseUint(c.Hash, 10, 64)
		cs = append(cs, uccWithCluster{Client: ucc, Name: c.Name, Cluster: sharedproto.Wrap(oracleClusterProto(c)), ClusterVersion: h})
	}
	for _, n := range input.Errored {
		cs = append(cs, uccWithCluster{Client: ucc, Name: n, Error: errors.New("fixture translation error")})
	}
	clusters := perClientClustersForSnapshotTest(t, uccs, krt.NewStaticCollection[uccWithCluster](nil, cs))
	eps, extra := []UccWithEndpoints{}, []UccWithEndpoints{}
	for _, e := range input.Endpoints {
		h, _ := strconv.ParseUint(e.Hash, 10, 64)
		row := UccWithEndpoints{Client: ucc, Endpoints: sharedproto.Wrap(oracleEndpointProto(e)), EndpointsHash: h, endpointsName: e.Name}
		if slices.Contains(input.Bootstrap, e.Name) {
			extra = append(extra, row)
		} else {
			eps = append(eps, row)
		}
	}
	collection := func(rows []UccWithEndpoints) PerClientEnvoyEndpoints {
		col := krt.NewStaticCollection[UccWithEndpoints](nil, rows)
		return PerClientEnvoyEndpoints{endpoints: col, index: krtpkg.UnnamedIndex(col, func(e UccWithEndpoints) []string { return []string{e.Client.ResourceName()} })}
	}
	gws := []GatewayXdsResources{}
	if input.Gateway {
		r, l, s := oracleConfigResources(input.Config)
		gws = append(gws, GatewayXdsResources{NamespacedName: types.NamespacedName{Namespace: "oracle", Name: client},
			Routes: r, Listeners: l, Secrets: s, ReferencedClusters: collectReferencedClusters(r, l)})
	}
	snaps := snapshotPerClient(opts, uccs, krt.NewStaticCollection[GatewayXdsResources](nil, gws), collection(eps), clusters, collection(extra))
	if !snaps.WaitUntilSynced(ctx.Done()) {
		t.Fatal("snapshotPerClient did not sync")
	}
	rows := snaps.List()
	if len(rows) == 0 {
		return nil
	}
	if len(rows) != 1 {
		t.Fatalf("unexpected snapshot count %d", len(rows))
	}
	return &rows[0]
}

func TestPublicationOracleKRT(t *testing.T) {
	o := startPublicationOracle(t)
	for name, r := range oracleExamples() {
		t.Run(name, func(t *testing.T) {
			want, err := o.predict(r)
			if err != nil {
				t.Fatal(err)
			}
			for k, e := range r.Events {
				if e.Kind != "build" {
					continue
				}
				t.Run(fmt.Sprintf("candidate-%d", e.ID), func(t *testing.T) {
					actual := oracleBuildKRT(t, *e.Input, e.Client)
					if err := oracleCompareCandidate(actual, want[k].Candidate); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
