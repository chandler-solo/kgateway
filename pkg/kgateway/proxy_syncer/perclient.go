package proxy_syncer

import (
	"fmt"
	"maps"
	"sort"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytcpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"istio.io/istio/pkg/kube/controllers"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

type clustersWithErrors struct {
	// +noKrtEquals
	clusters envoycache.Resources
	// +noKrtEquals
	erroredClusters     []string
	erroredClustersHash uint64
	clustersHash        uint64
	resourceName        string
}

type endpointsWithUccName struct {
	endpoints    envoycache.Resources
	resourceName string
}

func (c clustersWithErrors) ResourceName() string {
	return c.resourceName
}

var _ krt.Equaler[clustersWithErrors] = new(clustersWithErrors)

func (c clustersWithErrors) Equals(k clustersWithErrors) bool {
	return c.clustersHash == k.clustersHash && c.erroredClustersHash == k.erroredClustersHash && c.resourceName == k.resourceName
}

func (c endpointsWithUccName) ResourceName() string {
	return c.resourceName
}

var _ krt.Equaler[endpointsWithUccName] = new(endpointsWithUccName)

func (c endpointsWithUccName) Equals(k endpointsWithUccName) bool {
	return c.endpoints.Version == k.endpoints.Version && c.resourceName == k.resourceName
}

func snapshotPerClient(
	krtopts krtutil.KrtOptions,
	uccCol krt.Collection[ir.UniquelyConnectedClient],
	mostXdsSnapshots krt.Collection[GatewayXdsResources],
	endpoints PerClientEnvoyEndpoints,
	clusters PerClientEnvoyClusters,
	extraEndpointCollections ...PerClientEnvoyEndpoints,
) krt.Collection[XdsSnapWrapper] {
	clusterSnapshot := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) *clustersWithErrors {
		clustersForUcc := clusters.FetchClustersForClient(kctx, ucc)
		if len(clustersForUcc) == 0 {
			// Returning nil leaves no row for this UCC; the snapshot transform
			// below substitutes an empty cluster set and still publishes.
			logger.Info("no perclient clusters for client", "client", ucc.ResourceName())
			return nil
		}
		logger.Debug("found perclient clusters", "client", ucc.ResourceName(), "clusters", len(clustersForUcc))

		clustersProto := make([]envoycachetypes.ResourceWithTTL, 0, len(clustersForUcc))
		var (
			clustersHash        uint64
			erroredClustersHash uint64
			erroredClusters     []string
		)
		for _, c := range clustersForUcc {
			if c.Error != nil {
				erroredClusters = append(erroredClusters, c.Name)
				// For errored clusters, we don't want to include the cluster version
				// in the hash. The cluster version is the hash of the proto. because this cluster
				// won't be sent to envoy anyway, there's no point trigger updates if it changes from
				// one error state to a different error state.
				erroredClustersHash ^= utils.HashString(c.Name)
				continue
			}
			clustersProto = append(clustersProto, envoycachetypes.ResourceWithTTL{Resource: c.Cluster})
			clustersHash ^= c.ClusterVersion
		}
		clustersVersion := fmt.Sprintf("%d", clustersHash)

		clusterResources := envoycache.NewResourcesWithTTL(clustersVersion, clustersProto)

		return &clustersWithErrors{
			clusters:            clusterResources,
			erroredClusters:     erroredClusters,
			clustersHash:        clustersHash,
			erroredClustersHash: erroredClustersHash,
			resourceName:        ucc.ResourceName(),
		}
	}, krtopts.ToOptions("ClusterResources")...)

	endpointResources := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) *endpointsWithUccName {
		endpointsForUcc := endpoints.FetchEndpointsForClient(kctx, ucc)
		for _, extraEndpoints := range extraEndpointCollections {
			endpointsForUcc = append(endpointsForUcc, extraEndpoints.FetchEndpointsForClient(kctx, ucc)...)
		}
		endpointsProto := make([]envoycachetypes.ResourceWithTTL, 0, len(endpointsForUcc))
		var endpointsHash uint64
		for _, ep := range endpointsForUcc {
			endpointsProto = append(endpointsProto, envoycachetypes.ResourceWithTTL{Resource: ep.Endpoints})
			endpointsHash ^= ep.EndpointsHash
		}

		endpointResources := envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", endpointsHash), endpointsProto)
		return &endpointsWithUccName{
			endpoints:    endpointResources,
			resourceName: ucc.ResourceName(),
		}
	}, krtopts.ToOptions("EndpointResources")...)

	xdsSnapshotsForUcc := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) *XdsSnapWrapper {
		defer (collectXDSTransformMetrics(ucc.ResourceName()))(nil)

		listenerRouteSnapshot := krt.FetchOne(kctx, mostXdsSnapshots, krt.FilterKey(ucc.Role))
		if listenerRouteSnapshot == nil {
			logger.Debug("snapshot missing", "proxy_key", ucc.Role)
			return nil
		}
		clustersForUcc := krt.FetchOne(kctx, clusterSnapshot, krt.FilterKey(ucc.ResourceName()))
		clientEndpointResources := krt.FetchOne(kctx, endpointResources, krt.FilterKey(ucc.ResourceName()))

		// Build the snapshot and record, per cluster, whether its referenced
		// inputs are ready. One transform-level guard remains:
		//
		//  1. If per-client endpoints haven't been derived yet for this UCC,
		//     return nil. This handler can fire before the per-client
		//     collections — driven by the same upstream events — have
		//     re-run, so FetchOne may briefly return nil even though results
		//     are imminent. clustersForUcc is treated differently as a
		//     defensive measure: a nil clusterSnapshot entry can in principle
		//     mean either "not yet processed" or "this UCC legitimately has
		//     zero backend clusters". In practice the latter is hard to hit
		//     because finalBackends pulls in a BackendObjectIR for every
		//     Service port in the cluster (kube-apiserver, kube-dns, the
		//     gateway's own Service, etc.), so clustersForUcc is virtually
		//     never nil in an operational cluster — even for a gateway whose
		//     HTTPRoutes only emit RequestRedirect or direct responses.
		//     Substituting an empty resource set keeps the function honest
		//     against narrow edge cases (a freshly started controller before
		//     Service informers have synced, or a configuration whose only
		//     backends are non-K8s and all fail translation).
		//
		// The two former whole-snapshot readiness guards are now per-cluster
		// annotations on the wrapper instead of reasons to withhold the row:
		//
		//  2. A cluster referenced as a dataplane routing target
		//     (RouteAction / TcpProxy) that is not present or is explicitly
		//     errored is recorded in missingReferenced (see
		//     findMissingReferencedClusters below). Publishing a route that
		//     names such a cluster would cause Envoy to return 500/NC.
		//
		//  3. A referenced EDS cluster whose ClusterLoadAssignment was NOT
		//     derived by the per-client endpoints collection — a synthesized
		//     empty stands in — is recorded in missingEndpointsReferenced.
		//     For kube Service backends this can only be derivation lag: the
		//     endpoints transform emits a row for every resolvable Service
		//     port even with zero EndpointSlices (see NewK8sEndpoints), so an
		//     absent CLA means the per-client endpoints derivation has not
		//     caught up with the cluster (a just-added backend, a rebuild).
		//     The persistent variant is a plugin gap: a plugin contributing
		//     an EDS cluster without a matching endpoints-collection row.
		//     Flipping a route onto such a cluster now could drop all hosts
		//     for a backend that actually has some. PRESENCE, not contents,
		//     is the test: a derived CLA that is empty (or all unhealthy) is
		//     the backend's TRUTH — "route-referenced backends that are empty
		//     forever, on purpose" (ExternalName, scale-to-zero) is a config
		//     shape proven in production on the pre-per-cluster gates, whose
		//     guard was also an existence check — so it never marks the
		//     wrapper deferred, never taxes cold pods with the publish
		//     budget, and never holds a route flip; routes to such a backend
		//     fail until it has endpoints, exactly as they do today (#14352).
		//     Holding flips onto derived-but-empty clusters
		//     (make-before-break for an A->B retarget whose pods are still
		//     starting) is deliberately surrendered for stock parity; it can
		//     return later as an OPT-IN budget-bounded hold without
		//     revisiting this classification.
		//
		// A wrapper with either list non-empty is marked deferred, and
		// syncXds resolves it per cluster against the currently-published
		// snapshot: previously-published clusters are carried forward
		// (make-before-break), previously-referenced clusters whose CLA row
		// vanished publish the synthesized empty (their slices are gone —
		// that is the truth), and only a route flip onto a newly-referenced
		// not-yet-derived cluster is held back — bounded by the publish
		// budget (see publishGate), after which the flip goes out and routes
		// to still-unready clusters fail until they become ready. An
		// unresolvable reference — a plugin bug, a backend that never derives
		// endpoints — therefore degrades only the routes that name it: other
		// updates keep flowing, except route/listener/secret updates, which a
		// held flip pins for at most one budget.
		//
		// BackendRef typos never reach these checks as real cluster names:
		// IR-time resolution substitutes wellknown.BlackholeClusterName,
		// which findMissingReferencedClusters explicitly skips.
		//
		// Historical context: https://github.com/solo-io/gloo/pull/10611.
		if clustersForUcc == nil {
			clustersForUcc = &clustersWithErrors{
				clusters: envoycache.Resources{
					Items: map[string]envoycachetypes.ResourceWithTTL{},
				},
			}
		}
		if clientEndpointResources == nil {
			logger.Info("per-client endpoints not ready; deferring snapshot", "client", ucc.ResourceName())
			return nil
		}

		logger.Debug("found perclient clusters", "client", ucc.ResourceName(), "clusters", len(clustersForUcc.clusters.Items))
		clusterResources := clustersForUcc.clusters

		snap := XdsSnapWrapper{}
		if len(listenerRouteSnapshot.Clusters) > 0 {
			clustersProto := make(map[string]envoycachetypes.ResourceWithTTL, len(listenerRouteSnapshot.Clusters)+len(clustersForUcc.clusters.Items))
			maps.Copy(clustersProto, clustersForUcc.clusters.Items)
			for _, item := range listenerRouteSnapshot.Clusters {
				clustersProto[envoycache.GetResourceName(item.Resource)] = item
			}
			clusterResources.Version = fmt.Sprintf("%d", clustersForUcc.clustersHash^listenerRouteSnapshot.ClustersHash)
			clusterResources.Items = clustersProto
		}
		missingClusters := findMissingReferencedClusters(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			clustersForUcc.erroredClusters,
		)
		// Keep EDS resources aligned with the EDS clusters in the same CDS snapshot.
		// Envoy's named EDS requests are induced by CDS; stale CLAs for clusters no
		// longer present in CDS can make go-control-plane suppress ADS responses.
		endpointRes, synthesizedEndpoints := filterEndpointResourcesForClusters(clusterResources, clientEndpointResources.endpoints)
		// Post-synthesis every EDS cluster has a CLA; the synthesized set
		// identifies exactly the referenced clusters whose CLA was not
		// derived (a derived-but-empty CLA is truth, not a gap).
		missingEndpointClusters := findMissingReferencedEndpointResources(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			synthesizedEndpoints,
			clustersForUcc.erroredClusters,
		)

		snap.deferred = len(missingClusters) > 0 || len(missingEndpointClusters) > 0
		snap.missingReferenced = missingClusters
		snap.missingEndpointsReferenced = missingEndpointClusters
		snap.erroredClusters = clustersForUcc.erroredClusters
		snap.proxyKey = ucc.ResourceName()
		snapshot := &envoycache.Snapshot{}
		snapshot.Resources[envoycachetypes.Cluster] = clusterResources
		snapshot.Resources[envoycachetypes.Endpoint] = endpointRes
		snapshot.Resources[envoycachetypes.Route] = listenerRouteSnapshot.Routes
		snapshot.Resources[envoycachetypes.Listener] = listenerRouteSnapshot.Listeners
		snapshot.Resources[envoycachetypes.Secret] = listenerRouteSnapshot.Secrets
		// envoycache.NewResources(version, resource)
		snap.snap = snapshot
		if snap.deferred {
			logger.Info(
				"snapshot has unready referenced clusters; syncXds will resolve per cluster",
				"client", ucc.ResourceName(),
				"missing_clusters", missingClusters,
				"missing_endpoint_clusters", missingEndpointClusters,
			)
		}
		logger.Debug("snapshots", "proxy_key", snap.proxyKey,
			"listeners", resourcesStringer(listenerRouteSnapshot.Listeners).String(),
			"clusters", resourcesStringer(clusterResources).String(),
			"routes", resourcesStringer(listenerRouteSnapshot.Routes).String(),
			"endpoints", resourcesStringer(endpointRes).String(),
			"secrets", resourcesStringer(listenerRouteSnapshot.Secrets).String(),
		)

		return &snap
	}, krtopts.ToOptions("PerClientXdsSnapshots")...)

	metrics.RegisterEvents(xdsSnapshotsForUcc, func(o krt.Event[XdsSnapWrapper]) {
		cd := getDetailsFromXDSClientResourceName(o.Latest().ResourceName())

		switch o.Event {
		case controllers.EventDelete:
			snapshotResources.Set(0, snapshotResourcesMetricLabels{
				Gateway:   cd.Gateway,
				Namespace: cd.Namespace,
				Resource:  "Cluster",
			}.toMetricsLabels()...)

			snapshotResources.Set(0, snapshotResourcesMetricLabels{
				Gateway:   cd.Gateway,
				Namespace: cd.Namespace,
				Resource:  "Endpoint",
			}.toMetricsLabels()...)

			snapshotResources.Set(0, snapshotResourcesMetricLabels{
				Gateway:   cd.Gateway,
				Namespace: cd.Namespace,
				Resource:  "Route",
			}.toMetricsLabels()...)

			snapshotResources.Set(0, snapshotResourcesMetricLabels{
				Gateway:   cd.Gateway,
				Namespace: cd.Namespace,
				Resource:  "Listener",
			}.toMetricsLabels()...)

			snapshotResources.Set(0, snapshotResourcesMetricLabels{
				Gateway:   cd.Gateway,
				Namespace: cd.Namespace,
				Resource:  "Secret",
			}.toMetricsLabels()...)

		case controllers.EventAdd, controllers.EventUpdate:
			snapshotResources.Set(float64(len(o.Latest().snap.Resources[envoycachetypes.Cluster].Items)),
				snapshotResourcesMetricLabels{
					Gateway:   cd.Gateway,
					Namespace: cd.Namespace,
					Resource:  "Cluster",
				}.toMetricsLabels()...)

			snapshotResources.Set(float64(len(o.Latest().snap.Resources[envoycachetypes.Endpoint].Items)),
				snapshotResourcesMetricLabels{
					Gateway:   cd.Gateway,
					Namespace: cd.Namespace,
					Resource:  "Endpoint",
				}.toMetricsLabels()...)

			snapshotResources.Set(float64(len(o.Latest().snap.Resources[envoycachetypes.Route].Items)),
				snapshotResourcesMetricLabels{
					Gateway:   cd.Gateway,
					Namespace: cd.Namespace,
					Resource:  "Route",
				}.toMetricsLabels()...)

			snapshotResources.Set(float64(len(o.Latest().snap.Resources[envoycachetypes.Listener].Items)),
				snapshotResourcesMetricLabels{
					Gateway:   cd.Gateway,
					Namespace: cd.Namespace,
					Resource:  "Listener",
				}.toMetricsLabels()...)

			snapshotResources.Set(float64(len(o.Latest().snap.Resources[envoycachetypes.Secret].Items)),
				snapshotResourcesMetricLabels{
					Gateway:   cd.Gateway,
					Namespace: cd.Namespace,
					Resource:  "Secret",
				}.toMetricsLabels()...)
		}
	})

	return xdsSnapshotsForUcc
}

// collectReferencedClusters returns the set of cluster names referenced as
// dataplane routing targets (RouteAction and TcpProxy cluster / weighted-
// cluster specifiers) by the given routes and listeners. It walks typed_config
// extensions via protoreflect so it stays correct as Envoy adds new filter
// types that embed dataplane-target clusters.
//
// Scope is intentionally narrowed to dataplane targets. Ancillary cluster
// references (access-log GrpcService, JWT jwks HttpUri, ext_authz cluster,
// ratelimit cluster, etc.) are deliberately ignored because:
//
//  1. The plugin that emits the filter is responsible for also emitting the
//     ancillary cluster in the same per-gateway snapshot's ExtraClusters,
//     so there is no reconnect race between listener and cluster — they
//     arrive coherent or not at all.
//  2. If a plugin emits an ancillary reference without declaring the
//     cluster, that is a plugin bug. Gating on it would starve the entire
//     gateway forever; publishing and letting the filter fail (or degrade
//     per its failure_mode_allow) surfaces the bug without blocking valid
//     traffic.
//
// This is computed once per GatewayXdsResources (shared across all connected
// clients for that role) rather than per client — the proto walk and Any
// unmarshalling are non-trivial on large LDS/RDS.
func collectReferencedClusters(routes, listeners envoycache.Resources) map[string]struct{} {
	referenced := make(map[string]struct{})
	collectResourceClusterReferences(routes, referenced)
	collectResourceClusterReferences(listeners, referenced)
	return referenced
}

func findMissingReferencedClusters(
	referencedClusters map[string]struct{},
	clusters map[string]envoycachetypes.ResourceWithTTL,
	erroredClusters []string,
) []string {
	erroredClusterSet := stringSet(erroredClusters)

	missingClusters := make([]string, 0, len(referencedClusters))
	for name := range referencedClusters {
		if _, ok := clusters[name]; ok {
			continue
		}
		if _, ok := erroredClusterSet[name]; ok {
			continue
		}
		if name == wellknown.BlackholeClusterName {
			continue
		}
		missingClusters = append(missingClusters, name)
	}
	sort.Strings(missingClusters)

	return missingClusters
}

// findMissingReferencedEndpointResources reports the referenced EDS clusters
// whose ClusterLoadAssignment was not derived by the per-client endpoints
// collection — i.e. their CLA in the snapshot is a synthesized empty
// placeholder (see filterEndpointResourcesForClusters). Whether such a
// backend has endpoints is UNKNOWN — per-client derivation lag for kube
// Services (whose endpoints transform emits a row for every resolvable
// port, even sliceless ones like ExternalName), or a plugin that
// contributed an EDS cluster without an endpoints row — which is what
// warrants deferral. PRESENCE, not contents, is the test: a derived CLA
// with zero usable endpoints is the backend's known truth (scale-to-zero
// and crashlooping backends are steady states, not races — #14352) and must
// not defer the snapshot; this matches the existence semantics of the
// whole-snapshot gate this replaced, the behavior production configs are
// built against.
func findMissingReferencedEndpointResources(
	referencedClusters map[string]struct{},
	clusters map[string]envoycachetypes.ResourceWithTTL,
	synthesizedEndpoints map[string]struct{},
	erroredClusters []string,
) []string {
	erroredClusterSet := stringSet(erroredClusters)

	missingEndpointClusters := make([]string, 0, len(referencedClusters))
	for name := range referencedClusters {
		if _, ok := erroredClusterSet[name]; ok {
			continue
		}
		if name == wellknown.BlackholeClusterName {
			continue
		}

		clusterResource, ok := clusters[name]
		if !ok {
			continue
		}
		endpointResourceName, requiresEndpointResource := endpointResourceNameForCluster(clusterResource)
		if !requiresEndpointResource {
			continue
		}
		if _, synthesized := synthesizedEndpoints[endpointResourceName]; !synthesized {
			continue
		}
		missingEndpointClusters = append(missingEndpointClusters, name)
	}
	sort.Strings(missingEndpointClusters)

	return missingEndpointClusters
}

func endpointResourceNameForCluster(resource envoycachetypes.ResourceWithTTL) (string, bool) {
	cluster, ok := resource.Resource.(*envoyclusterv3.Cluster)
	if !ok {
		return "", false
	}
	clusterType, ok := cluster.GetClusterDiscoveryType().(*envoyclusterv3.Cluster_Type)
	if !ok || clusterType.Type != envoyclusterv3.Cluster_EDS {
		return "", false
	}
	if edsServiceName := cluster.GetEdsClusterConfig().GetServiceName(); edsServiceName != "" {
		return edsServiceName, true
	}
	return cluster.GetName(), true
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func collectResourceClusterReferences(resources envoycache.Resources, referencedClusters map[string]struct{}) {
	for _, item := range resources.Items {
		if item.Resource == nil {
			continue
		}
		collectProtoClusterReferences(item.Resource, referencedClusters)
	}
}

func collectProtoClusterReferences(msg proto.Message, referencedClusters map[string]struct{}) {
	if msg == nil {
		return
	}

	switch typedMsg := msg.(type) {
	case *envoyroutev3.RouteAction:
		switch clusterSpecifier := typedMsg.GetClusterSpecifier().(type) {
		case *envoyroutev3.RouteAction_Cluster:
			if clusterSpecifier.Cluster != "" {
				referencedClusters[clusterSpecifier.Cluster] = struct{}{}
			}
		case *envoyroutev3.RouteAction_WeightedClusters:
			if clusterSpecifier.WeightedClusters == nil {
				break
			}
			for _, cluster := range clusterSpecifier.WeightedClusters.GetClusters() {
				if cluster.GetName() != "" {
					referencedClusters[cluster.GetName()] = struct{}{}
				}
			}
		}
	case *envoytcpv3.TcpProxy:
		switch clusterSpecifier := typedMsg.GetClusterSpecifier().(type) {
		case *envoytcpv3.TcpProxy_Cluster:
			if clusterSpecifier.Cluster != "" {
				referencedClusters[clusterSpecifier.Cluster] = struct{}{}
			}
		case *envoytcpv3.TcpProxy_WeightedClusters:
			if clusterSpecifier.WeightedClusters == nil {
				break
			}
			for _, cluster := range clusterSpecifier.WeightedClusters.GetClusters() {
				if cluster.GetName() != "" {
					referencedClusters[cluster.GetName()] = struct{}{}
				}
			}
		}
	}

	collectNestedProtoClusterReferences(msg.ProtoReflect(), referencedClusters)
}

func collectNestedProtoClusterReferences(
	msg protoreflect.Message,
	referencedClusters map[string]struct{},
) {
	if !msg.IsValid() {
		return
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				collectProtoClusterReferencesFromValue(list.Get(i), referencedClusters)
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			m := v.Map()
			m.Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
				collectProtoClusterReferencesFromValue(value, referencedClusters)
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			collectProtoClusterReferencesFromValue(v, referencedClusters)
		}
		return true
	})
}

func collectProtoClusterReferencesFromValue(v protoreflect.Value, referencedClusters map[string]struct{}) {
	msg := v.Message()
	if !msg.IsValid() {
		return
	}

	if anyMsg, ok := msg.Interface().(*anypb.Any); ok {
		nestedMsg, err := anyMsg.UnmarshalNew()
		if err != nil {
			// Typed extensions whose Go types aren't linked into this binary will fail here;
			// that's expected, but log at debug so genuinely malformed configs are diagnosable.
			logger.Debug("skipping typed_config during cluster reference scan", "type_url", anyMsg.GetTypeUrl(), "error", err)
			return
		}
		collectProtoClusterReferences(nestedMsg, referencedClusters)
		return
	}

	collectProtoClusterReferences(msg.Interface(), referencedClusters)
}

// filterEndpointResourcesForClusters returns the EDS resource set that exactly
// matches the EDS clusters in the same CDS snapshot: it drops CLAs for STATIC
// clusters and for EDS clusters no longer in CDS (Envoy requests EDS resources
// from CDS, so a stale CLA can make the ADS cache refuse named EDS responses),
// and it synthesizes an empty ClusterLoadAssignment for any EDS cluster that
// has no derived CLA. The result keeps the published snapshot EDS-consistent —
// every EDS cluster has exactly one CLA and there are no CLAs without a
// cluster (go-control-plane's Snapshot.Consistent() invariant) — rather than
// relying on the cache tolerating a dangling EDS cluster, and it lets Envoy
// treat such a cluster as active-with-no-hosts immediately instead of stalling
// its warming on an absent EDS resource until the initial-fetch timeout.
//
// The second return value is the set of endpoint resource names that were
// synthesized. Referenced clusters backed by a synthesized CLA mark the
// wrapper deferred (classifyReferencedEndpointResources) so a route flip
// does not land on a cluster whose endpoints simply have not been derived
// yet; synthesized empties still reach Envoy for clusters no route targets,
// and on the bounded publish paths (publishGate), where active-with-no-hosts
// is the correct interim state.
func filterEndpointResourcesForClusters(clusters envoycache.Resources, endpoints envoycache.Resources) (envoycache.Resources, map[string]struct{}) {
	requiredEndpointNames := make(map[string]struct{})
	for _, item := range clusters.Items {
		if endpointName, requiresEndpointResource := endpointResourceNameForCluster(item); requiresEndpointResource {
			requiredEndpointNames[endpointName] = struct{}{}
		}
	}
	covered := make(map[string]struct{}, len(requiredEndpointNames))
	filteredEndpoints := make([]envoycachetypes.ResourceWithTTL, 0, len(endpoints.Items))
	var resourcesHash uint64
	for _, item := range endpoints.Items {
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		if !ok {
			continue
		}
		if _, required := requiredEndpointNames[cla.GetClusterName()]; !required {
			continue
		}
		filteredEndpoints = append(filteredEndpoints, item)
		covered[cla.GetClusterName()] = struct{}{}
		resourcesHash ^= utils.HashProto(cla)
	}
	// Synthesize empty assignments for EDS clusters that have no derived CLA
	// so the published snapshot stays EDS-consistent.
	synthesized := make(map[string]struct{})
	for name := range requiredEndpointNames {
		if _, ok := covered[name]; ok {
			continue
		}
		empty := &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}
		filteredEndpoints = append(filteredEndpoints, envoycachetypes.ResourceWithTTL{Resource: empty})
		resourcesHash ^= utils.HashProto(empty)
		synthesized[name] = struct{}{}
	}
	if len(synthesized) == 0 && len(filteredEndpoints) == len(endpoints.Items) {
		return endpoints, nil
	}
	return envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", resourcesHash), filteredEndpoints), synthesized
}
