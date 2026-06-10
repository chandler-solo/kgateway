package proxy_syncer

import (
	"fmt"
	"maps"
	"sort"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytcpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
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
	// claHashes carries the per-CLA equality hashes computed by the endpoints
	// pipeline, keyed by CLA name, so the snapshot transform can derive the
	// published EDS version without re-marshaling every CLA. Derived from the
	// same inputs as endpoints (covered by its Version in Equals).
	// +noKrtEquals
	claHashes map[string]uint64
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
	uccCol krt.Collection[ir.UniqlyConnectedClient],
	mostXdsSnapshots krt.Collection[GatewayXdsResources],
	endpoints PerClientEnvoyEndpoints,
	clusters PerClientEnvoyClusters,
	heartbeat *krt.RecomputeTrigger,
) krt.Collection[XdsSnapWrapper] {
	// Every transform on the per-client path marks the heartbeat, not just the
	// leaf collections: the event-delivery gap that motivates the heartbeat
	// (#14184) can drop a recompute on ANY edge between collections, and a
	// heartbeat that re-runs only the leaves cannot heal a stale row downstream
	// of them — an unchanged leaf recompute is hash-suppressed and propagates
	// nothing. Marking each transform makes a tick re-run the whole path; when
	// nothing changed, the outputs hash-equal and KRT suppresses the churn.
	clusterSnapshot := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) *clustersWithErrors {
		if heartbeat != nil {
			heartbeat.MarkDependant(kctx)
		}
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

	endpointResources := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) *endpointsWithUccName {
		if heartbeat != nil {
			heartbeat.MarkDependant(kctx)
		}
		endpointsForUcc := endpoints.FetchEndpointsForClient(kctx, ucc)
		endpointsProto := make([]envoycachetypes.ResourceWithTTL, 0, len(endpointsForUcc))
		claHashes := make(map[string]uint64, len(endpointsForUcc))
		var endpointsHash uint64
		for _, ep := range endpointsForUcc {
			endpointsProto = append(endpointsProto, envoycachetypes.ResourceWithTTL{Resource: ep.Endpoints})
			endpointsHash ^= ep.EndpointsHash
			claHashes[envoycache.GetResourceName(ep.Endpoints)] = ep.EndpointsHash
		}

		endpointResources := envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", endpointsHash), endpointsProto)
		return &endpointsWithUccName{
			endpoints:    endpointResources,
			resourceName: ucc.ResourceName(),
			claHashes:    claHashes,
		}
	}, krtopts.ToOptions("EndpointResources")...)

	xdsSnapshotsForUcc := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) *XdsSnapWrapper {
		defer (collectXDSTransformMetrics(ucc.ResourceName()))(nil)
		if heartbeat != nil {
			heartbeat.MarkDependant(kctx)
		}

		listenerRouteSnapshot := krt.FetchOne(kctx, mostXdsSnapshots, krt.FilterKey(ucc.Role))
		if listenerRouteSnapshot == nil {
			logger.Debug("snapshot missing", "proxy_key", ucc.Role)
			return nil
		}
		clustersForUcc := krt.FetchOne(kctx, clusterSnapshot, krt.FilterKey(ucc.ResourceName()))
		clientEndpointResources := krt.FetchOne(kctx, endpointResources, krt.FilterKey(ucc.ResourceName()))

		// Defer publishing a per-client snapshot until its per-client inputs
		// are coherent. This matters for both directions of the reconnect
		// race: a brand-new (or reconnecting) Envoy may already hold a full,
		// working config in memory, and a state-of-the-world publish of a
		// PARTIAL snapshot would REMOVE every resource not in it — an outage
		// on a healthy proxy. The control plane cannot see what the client
		// already has, so the only safe first snapshot is a consistent one.
		// Three guards:
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
		//     backends are non-K8s and all fail translation) without
		//     weakening guards 2 and 3, which still defer if any specific
		//     cluster reference is missing.
		//
		//  2. If any cluster referenced as a dataplane routing target
		//     (RouteAction / TcpProxy) is not yet present or explicitly
		//     errored, return nil (see findMissingReferencedClusters below).
		//     Publishing before then would emit a partial CDS referenced by
		//     listeners/routes and cause Envoy to return 500/NC on routes
		//     whose clusters just happen to be in the same CDS response.
		//
		//  3. If any referenced EDS cluster has no matching ClusterLoadAssignment
		//     in the EDS resources that would be sent, return nil. Publishing
		//     CDS/RDS/LDS before EDS catches up can make Envoy drop all hosts for
		//     a route that was healthy before a controller restart.
		//
		// Returning nil removes this UCC's entry from the output collection,
		// which surfaces as a Delete event in proxy_syncer.go's xDS
		// subscriber. That Delete branch is intentionally a no-op so the
		// xDS snapshot cache retains the last-published Snapshot for this
		// client. Envoy therefore keeps serving its previous, coherent
		// config until a new coherent snapshot overwrites it. This is what
		// prevents an unresolvable reference — a user BackendRef typo, a
		// plugin bug — from stranding Envoy: there is no error response on
		// valid traffic during the defer window, only continuity.
		//
		// The defers are bounded in practice by the per-client heartbeat
		// (#14184): deferred and connected-but-never-published clients count
		// as stuck, and each heartbeat tick re-runs the whole per-client
		// path against current inputs, so a defer caused by a missed or
		// stale recompute heals within one tick instead of persisting
		// indefinitely.
		//
		// BackendRef typos never reach this gate as real cluster names:
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
			recordSnapshotDefer(ucc.ResourceName(), deferReasonEndpointsNotReady)
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
			recordSnapshotDefer(ucc.ResourceName(), deferReasonMissingClusters)
			return nil
		}
		// S2 (EDS subset): publish exactly the CLAs required by EDS clusters in
		// this snapshot's CDS. Anything else (STATIC clusters' CLAs, stale CLAs
		// for clusters no longer in CDS) would make go-control-plane suppress
		// named state-of-the-world ADS EDS responses ("ADS mode: not responding
		// to request"), freezing endpoint delivery for the whole client even
		// after its inputs become coherent.
		endpointRes := filterEndpointResourcesForClusters(clusterResources, *clientEndpointResources)
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
			recordSnapshotDefer(ucc.ResourceName(), deferReasonMissingEndpoints)
			return nil
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

// findMissingReferencedEndpointResources returns the referenced EDS clusters
// (blackhole and errored clusters exempt) whose required CLA resource name is
// absent from endpoints — guard 3's predicate.
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
		if _, ok := endpoints[endpointResourceName]; ok {
			continue
		}
		missingEndpointClusters = append(missingEndpointClusters, name)
	}
	sort.Strings(missingEndpointClusters)

	return missingEndpointClusters
}

// filterEndpointResourcesForClusters returns only the CLAs required by EDS
// clusters present in clusters — the S2 (EDS subset) invariant. CLAs for STATIC
// clusters and stale CLAs for clusters no longer in CDS are dropped; either
// would make go-control-plane suppress named state-of-the-world ADS EDS
// responses, freezing endpoint delivery for the whole client.
//
// The returned version combines two signals, both load-bearing:
//
//   - the FILTERED content: the published CLA set changes whenever the cluster
//     set does — including a cluster being removed and later re-added with
//     unchanged endpoint inputs — and a version that does not change with it
//     leaves state-of-the-world EDS watches "up to date", so go-control-plane
//     never answers and Envoy stalls on initial_fetch_timeout;
//   - the upstream endpoints version: policy attachment bumps it without
//     changing CLA contents (see backendEndpointVersionHash) precisely so a
//     cluster re-warming after a CDS change receives a fresh EDS response;
//     deriving from content alone would erase that bump and reintroduce the
//     re-warm stall (envoyproxy/envoy#13009).
//
// Per-CLA hashes come precomputed from the endpoints pipeline (claHashes) —
// the same equality hashes whose changes are what cause this transform to
// re-run at all, so re-marshaling each CLA here would add cost without adding
// signal.
func filterEndpointResourcesForClusters(clusters envoycache.Resources, endpoints endpointsWithUccName) envoycache.Resources {
	required := requiredEndpointResourceNames(clusters.Items)
	filtered := make([]envoycachetypes.ResourceWithTTL, 0, len(endpoints.endpoints.Items))
	var contentHash uint64
	for name, item := range endpoints.endpoints.Items {
		if _, ok := required[name]; ok {
			filtered = append(filtered, item)
			if hash, ok := endpoints.claHashes[name]; ok {
				contentHash ^= hash
			} else {
				contentHash ^= utils.HashProto(item.Resource)
			}
		}
	}
	version := fmt.Sprintf("%d", contentHash^utils.HashString(endpoints.endpoints.Version))
	return envoycache.NewResourcesWithTTL(version, filtered)
}

// requiredEndpointResourceNames returns the set of CLA resource names Envoy will
// request given the EDS clusters present in clusters.
func requiredEndpointResourceNames(clusters map[string]envoycachetypes.ResourceWithTTL) map[string]struct{} {
	required := make(map[string]struct{}, len(clusters))
	for _, item := range clusters {
		if claName, isEDS := endpointResourceNameForCluster(item); isEDS {
			required[claName] = struct{}{}
		}
	}
	return required
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
	walkProtoMessages(msg, "cluster reference", func(m proto.Message) {
		switch typedMsg := m.(type) {
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
	})
}
