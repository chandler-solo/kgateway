package proxy_syncer

import (
	"fmt"
	"maps"
	"sort"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
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

// Defer reasons, attached to deferred XdsSnapWrappers for logs and metrics.
const (
	// deferReasonEndpointsNotReady: the per-client endpoints collection has
	// not produced a row for this client yet (initial sync or fan-out lag).
	deferReasonEndpointsNotReady = "endpoints_not_ready"
	// deferReasonMissingClusters: a cluster referenced as a dataplane routing
	// target is not present in the per-client cluster set.
	deferReasonMissingClusters = "missing_clusters"
	// deferReasonMissingEndpoints: a referenced EDS cluster has no
	// ClusterLoadAssignment in the endpoint set (an empty synthesized CLA is
	// substituted so the snapshot stays self-consistent).
	deferReasonMissingEndpoints = "missing_endpoints"
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
			logger.Info("no perclient clusters; defer building snapshot", "client", ucc.ResourceName())
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

		// Detect whether this client's per-client inputs are coherent. Three
		// guards (computation unchanged from the original defer gate):
		//
		//  1. Per-client endpoints haven't been derived yet for this UCC.
		//     This handler can fire before the per-client collections —
		//     driven by the same upstream events — have re-run, so FetchOne
		//     may briefly return nil even though results are imminent.
		//     clustersForUcc is treated differently as a defensive measure:
		//     a nil clusterSnapshot entry can in principle mean either "not
		//     yet processed" or "this UCC legitimately has zero backend
		//     clusters". In practice the latter is hard to hit because
		//     finalBackends pulls in a BackendObjectIR for every Service
		//     port in the cluster, so clustersForUcc is virtually never nil
		//     in an operational cluster.
		//
		//  2. A cluster referenced as a dataplane routing target
		//     (RouteAction / TcpProxy) is not yet present and not explicitly
		//     errored (see findMissingReferencedClusters below). Publishing
		//     that would emit a partial CDS referenced by listeners/routes
		//     and cause Envoy to return 500/NC on routes whose clusters just
		//     happen to be in the same CDS response.
		//
		//  3. A referenced EDS cluster has no matching ClusterLoadAssignment
		//     with at least one usable endpoint in the EDS resources that would
		//     be sent. A CLA with zero usable endpoints is not readiness:
		//     publishing CDS/RDS/LDS before EDS catches up can make Envoy warm
		//     the cluster onto no hosts and drop traffic that was healthy before
		//     the update.
		//
		// Unlike the original gate, a failed guard no longer withholds the
		// row (return nil): the snapshot is still built — with explicit empty
		// ClusterLoadAssignments synthesized for any missing EDS references so
		// it stays self-consistent — and marked deferred. The PUBLICATION
		// policy lives in syncXds: a client that already holds a published
		// snapshot never receives a deferred one (Envoy keeps its last
		// coherent config, indefinitely — same behavior as the old gate),
		// while a client that has never been published anything receives the
		// deferred snapshot once the first-publish budget expires, because for
		// that client the alternative is no listeners at all (a hard outage,
		// not staleness).
		//
		// BackendRef typos never reach these guards as real cluster names:
		// IR-time resolution substitutes wellknown.BlackholeClusterName,
		// which findMissingReferencedClusters explicitly skips.
		//
		// Historical context: https://github.com/solo-io/gloo/pull/10611.
		var deferReasons []string
		if clustersForUcc == nil {
			clustersForUcc = &clustersWithErrors{
				clusters: envoycache.Resources{
					Items: map[string]envoycachetypes.ResourceWithTTL{},
				},
			}
		}
		if clientEndpointResources == nil {
			logger.Info("per-client endpoints not ready; deferring snapshot", "client", ucc.ResourceName())
			deferReasons = append(deferReasons, deferReasonEndpointsNotReady)
			clientEndpointResources = &endpointsWithUccName{
				endpoints: envoycache.Resources{
					Items: map[string]envoycachetypes.ResourceWithTTL{},
				},
				resourceName: ucc.ResourceName(),
			}
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
		if missingClusters := findMissingReferencedClusters(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			clustersForUcc.erroredClusters,
		); len(missingClusters) > 0 {
			logger.Info(
				"defer building snapshot until all referenced clusters are ready",
				"client", ucc.ResourceName(),
				"missing_clusters", missingClusters,
			)
			deferReasons = append(deferReasons, deferReasonMissingClusters)
		}
		// Keep EDS resources aligned with the EDS clusters in this CDS snapshot.
		// Envoy's named EDS requests are induced by CDS; stale CLAs for clusters
		// no longer present in CDS can make go-control-plane suppress ADS
		// responses, pinning a proxy to its last (stale) endpoints.
		endpointRes := filterEndpointResourcesForClusters(clusterResources, clientEndpointResources.endpoints)
		if missingEndpointClusters := findMissingReferencedEndpointResources(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			endpointRes.Items,
			clustersForUcc.erroredClusters,
		); len(missingEndpointClusters) > 0 {
			logger.Info(
				"defer building snapshot until all referenced EDS resources are ready",
				"client", ucc.ResourceName(),
				"missing_endpoint_clusters", missingEndpointClusters,
			)
			deferReasons = append(deferReasons, deferReasonMissingEndpoints)
			// Synthesize explicit empty CLAs for the missing references so the
			// snapshot stays self-consistent if it ends up published to a
			// never-published client: an explicit empty assignment makes Envoy
			// treat the cluster as active-with-no-hosts immediately instead of
			// stalling cluster warming on an absent EDS resource. The names are
			// recorded on the wrapper so the carry-forward merge can prefer a
			// previously-published real CLA over the synthesized empty.
			endpointRes, snap.synthesizedClas = synthesizeEmptyEndpointResources(missingEndpointClusters, clusterResources.Items, endpointRes)
		}

		snap.deferred = len(deferReasons) > 0
		snap.deferReasons = deferReasons
		if snap.deferred {
			// Consumed by the carry-forward merge in syncXds; read-only there.
			snap.referencedClusters = listenerRouteSnapshot.ReferencedClusters
		}
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

func findMissingReferencedEndpointResources(
	referencedClusters map[string]struct{},
	clusters map[string]envoycachetypes.ResourceWithTTL,
	endpoints map[string]envoycachetypes.ResourceWithTTL,
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
		// A CLA with zero usable endpoints is not ready for make-before-break:
		// publishing a route onto a cluster Envoy would warm to no hosts drops
		// traffic that was healthy before the update.
		endpointResource, ok := endpoints[endpointResourceName]
		if ok && clusterLoadAssignmentHasUsableEndpoint(endpointResource) {
			continue
		}
		missingEndpointClusters = append(missingEndpointClusters, name)
	}
	sort.Strings(missingEndpointClusters)

	return missingEndpointClusters
}

// clusterLoadAssignmentHasUsableEndpoint reports whether a CLA has at least one
// addressable, non-UNHEALTHY endpoint — i.e. a cluster that can actually serve
// traffic, not a placeholder or all-drained assignment.
func clusterLoadAssignmentHasUsableEndpoint(resource envoycachetypes.ResourceWithTTL) bool {
	cla, ok := resource.Resource.(*envoyendpointv3.ClusterLoadAssignment)
	if !ok {
		return false
	}
	for _, locality := range cla.GetEndpoints() {
		for _, lbEndpoint := range locality.GetLbEndpoints() {
			if lbEndpoint.GetHealthStatus() == envoycorev3.HealthStatus_UNHEALTHY {
				continue
			}
			if lbEndpoint.GetEndpoint() != nil {
				return true
			}
		}
	}
	return false
}

// synthesizeEmptyEndpointResources returns a copy of endpointRes with an
// explicit empty ClusterLoadAssignment added for every cluster in
// missingEndpointClusters (resource-named per the cluster's EDS config). The
// version is extended with a hash of the synthesized names so a snapshot with
// synthesized entries can never share a version with one whose real CLAs
// arrived.
func synthesizeEmptyEndpointResources(
	missingEndpointClusters []string,
	clusters map[string]envoycachetypes.ResourceWithTTL,
	endpointRes envoycache.Resources,
) (envoycache.Resources, []string) {
	items := make(map[string]envoycachetypes.ResourceWithTTL, len(endpointRes.Items)+len(missingEndpointClusters))
	maps.Copy(items, endpointRes.Items)
	synthesized := make([]string, 0, len(missingEndpointClusters))
	var synthHash uint64
	for _, clusterName := range missingEndpointClusters {
		clusterResource, ok := clusters[clusterName]
		if !ok {
			continue
		}
		resourceName, requiresEndpointResource := endpointResourceNameForCluster(clusterResource)
		if !requiresEndpointResource {
			continue
		}
		items[resourceName] = envoycachetypes.ResourceWithTTL{
			Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: resourceName},
		}
		synthesized = append(synthesized, resourceName)
		synthHash ^= utils.HashString(resourceName)
	}
	sort.Strings(synthesized)
	return envoycache.Resources{
		Version: fmt.Sprintf("%s-synth-%d", endpointRes.Version, synthHash),
		Items:   items,
	}, synthesized
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

// filterEndpointResourcesForClusters returns endpoint resources required by EDS
// clusters in the same CDS snapshot. Envoy requests EDS resources from CDS, so
// publishing CLAs for STATIC clusters or removed EDS clusters can make the ADS
// cache refuse named EDS responses (leaving a proxy pinned to stale endpoints).
// The filtered set is versioned by content hash, so the EDS version changes
// whenever the set changes and go-control-plane always re-answers.
func filterEndpointResourcesForClusters(clusters envoycache.Resources, endpoints envoycache.Resources) envoycache.Resources {
	requiredEndpointNames := make(map[string]struct{})
	for _, item := range clusters.Items {
		if endpointName, requiresEndpointResource := endpointResourceNameForCluster(item); requiresEndpointResource {
			requiredEndpointNames[endpointName] = struct{}{}
		}
	}
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
		resourcesHash ^= utils.HashProto(cla)
	}
	if len(filteredEndpoints) == len(endpoints.Items) {
		return endpoints
	}
	return envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", resourcesHash), filteredEndpoints)
}
