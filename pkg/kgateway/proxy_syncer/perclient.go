package proxy_syncer

import (
	"maps"
	"slices"
	"strconv"

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
	endpoints envoycache.Resources
	// bootstrapEndpointNames are the CLA names contributed by the extra endpoint
	// collections passed to snapshotPerClient: resources whose cluster lives in the
	// Envoy bootstrap rather than in CDS (today, the zone-aware local cluster built
	// by NewPerClientLocalClusterEndpoints). filterEndpointResourcesForClusters keeps
	// them even though no CDS cluster names them.
	//
	// Not compared directly: a name is part of the CLA content hashed into
	// endpoints.Version, which Equals does compare.
	// +noKrtEquals
	bootstrapEndpointNames map[string]struct{}
	resourceName           string
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
		clustersVersion := strconv.FormatUint(clustersHash, 10)

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
		var bootstrapEndpointNames map[string]struct{}
		for _, extraEndpoints := range extraEndpointCollections {
			extraEndpointsForUcc := extraEndpoints.FetchEndpointsForClient(kctx, ucc)
			for _, ep := range extraEndpointsForUcc {
				if bootstrapEndpointNames == nil {
					bootstrapEndpointNames = make(map[string]struct{}, len(extraEndpointsForUcc))
				}
				bootstrapEndpointNames[ep.Endpoints.GetClusterName()] = struct{}{}
			}
			endpointsForUcc = append(endpointsForUcc, extraEndpointsForUcc...)
		}
		endpointsProto := make([]envoycachetypes.ResourceWithTTL, 0, len(endpointsForUcc))
		var endpointsHash uint64
		for _, ep := range endpointsForUcc {
			endpointsProto = append(endpointsProto, envoycachetypes.ResourceWithTTL{Resource: ep.Endpoints})
			endpointsHash ^= ep.EndpointsHash
		}

		endpointResources := envoycache.NewResourcesWithTTL(strconv.FormatUint(endpointsHash, 10), endpointsProto)
		return &endpointsWithUccName{
			endpoints:              endpointResources,
			bootstrapEndpointNames: bootstrapEndpointNames,
			resourceName:           ucc.ResourceName(),
		}
	}, krtopts.ToOptions("EndpointResources")...)

	xdsSnapshotsForUcc := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) *XdsSnapWrapper {
		defer (collectXDSTransformMetrics(ucc.ResourceName()))(nil)

		listenerRouteSnapshot := krt.FetchOne(kctx, mostXdsSnapshots, krt.FilterKey(ucc.Role))
		if listenerRouteSnapshot == nil {
			logger.Debug("snapshot missing", "proxy_key", ucc.Role)
			emitXdsSnapshotTrace(ucc.ResourceName(), xdsTraceDecisionDeferRoleSnapshot,
				nil, nil, envoycache.Resources{}, envoycache.Resources{})
			return nil
		}
		clustersForUcc := krt.FetchOne(kctx, clusterSnapshot, krt.FilterKey(ucc.ResourceName()))
		clientEndpointResources := krt.FetchOne(kctx, endpointResources, krt.FilterKey(ucc.ResourceName()))

		// Wait for the per-client endpoint collection to be derived; an empty
		// derived collection is valid. A missing cluster collection is treated
		// as empty, then checked against the actual route references below.
		//
		// CDS gaps and unusable endpoints are recorded on the wrapper. syncXds
		// defers first publication only for nonexempt missing CDS clusters;
		// synthesized or actual empty CLAs publish on first cache installation.
		// With an existing snapshot it retains the warm-transition policy:
		// carry missing old clusters, publish scale-to-zero truth, and hold a
		// flip to a newly referenced unready backend. That hold affects entire
		// route/listener/secret types and is not a general anti-starvation rule.
		//
		// Returning nil for missing input emits a collection Delete, whose
		// subscriber intentionally retains the previous cache entry. No cached
		// entry can mean a new proxy OR a controller restart with warm proxies.
		if clustersForUcc == nil {
			clustersForUcc = &clustersWithErrors{
				clusters: envoycache.Resources{
					Items: map[string]envoycachetypes.ResourceWithTTL{},
				},
			}
		}
		if clientEndpointResources == nil {
			logger.Info("per-client endpoints not ready; deferring snapshot", "client", ucc.ResourceName())
			emitXdsSnapshotTrace(ucc.ResourceName(), xdsTraceDecisionDeferEndpointsNotReady,
				listenerRouteSnapshot.ReferencedClusters, clustersForUcc.erroredClusters,
				clustersForUcc.clusters, envoycache.Resources{})
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
			clusterResources.Version = strconv.FormatUint(clustersForUcc.clustersHash^listenerRouteSnapshot.ClustersHash, 10)
			clusterResources.Items = clustersProto
		}
		// Per-cluster readiness (spec: devel/formal/lean/XdsSpec/PerClusterReadiness.lean).
		// A referenced cluster that is missing from CDS or has no usable
		// endpoint no longer defers the whole snapshot; the gaps are recorded
		// on the wrapper and syncXds resolves them per cluster against the
		// currently-published snapshot: carry forward what was published
		// before (make-before-break), publish the truth for previously-
		// referenced clusters that scaled to zero, and hold only a route flip
		// onto a newly-referenced not-yet-ready cluster.
		missingClusters := findMissingReferencedClusters(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			clustersForUcc.erroredClusters,
		)
		// Keep EDS resources aligned with the EDS clusters in the same CDS snapshot.
		// Envoy's named EDS requests are induced by CDS; stale CLAs for clusters no
		// longer present in CDS can make go-control-plane suppress ADS responses.
		endpointRes := filterEndpointResourcesForClusters(
			clusterResources,
			clientEndpointResources.endpoints,
			clientEndpointResources.bootstrapEndpointNames,
		)
		// Post-synthesis every EDS cluster has a CLA, so this reports exactly
		// the referenced clusters whose CLA has no usable endpoint.
		unusableClusters := findMissingReferencedEndpointResources(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			endpointRes.Items,
			clustersForUcc.erroredClusters,
		)

		snap.deferred = len(missingClusters) > 0 || len(unusableClusters) > 0
		snap.missingReferenced = missingClusters
		snap.unusableReferenced = unusableClusters
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
				"unusable_clusters", unusableClusters,
			)
			emitXdsSnapshotTrace(ucc.ResourceName(), xdsTraceDecisionDeferFlip,
				listenerRouteSnapshot.ReferencedClusters, clustersForUcc.erroredClusters,
				clusterResources, endpointRes)
		} else {
			emitXdsSnapshotTrace(ucc.ResourceName(), xdsTraceDecisionPublish,
				listenerRouteSnapshot.ReferencedClusters, clustersForUcc.erroredClusters,
				clusterResources, endpointRes)
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
	slices.Sort(missingClusters)

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
		endpointResource, ok := endpoints[endpointResourceName]
		if ok && clusterLoadAssignmentHasUsableEndpoint(endpointResource) {
			continue
		}
		missingEndpointClusters = append(missingEndpointClusters, name)
	}
	slices.Sort(missingEndpointClusters)

	return missingEndpointClusters
}

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
// clusters and for EDS clusters no longer in CDS (Envoy requests EDS names
// from CDS, so a stale CLA can make the ADS cache refuse named EDS responses),
// and it synthesizes an empty ClusterLoadAssignment for any EDS cluster that
// has no CLA yet. The synthesis keeps the published snapshot EDS-consistent —
// every EDS cluster has exactly one CLA and there are no CLAs without a
// cluster (go-control-plane's Snapshot.Consistent() invariant) — rather than
// relying on the cache tolerating a dangling EDS cluster, and it lets Envoy
// treat such a cluster as active-with-no-hosts immediately instead of stalling
// its warming on an absent EDS resource until the initial-fetch timeout.
// Referenced clusters whose only CLA is a synthesized empty are marked unready
// by findMissingReferencedEndpointResources, which checks for a
// usable endpoint; syncXds permits referenced empty CLAs on first publication
// and for previously referenced backends, but holds warm flips to new backends.
//
// Clusters dropped from CDS because backend translation failed are covered by
// the same rule: their CLA is not required by any published cluster, so it goes
// with them. That matters for more than tidiness — in ADS mode a single CLA the
// proxy never subscribed to makes go-control-plane's superset() check withhold
// the entire EDS response, freezing endpoints for every healthy cluster on that
// proxy until it reconnects.
//
// The version is a content hash of the resources actually published, which is
// what makes an error -> recovery flap visible: go-control-plane's SOTW push
// path only responds when the snapshot version differs from the watch's
// last-acked version, so a recovered cluster whose endpoints never changed
// would otherwise get no EDS push and stay warming until some unrelated
// endpoint change bumped the version.
//
// bootstrapEndpointNames lists CLAs whose cluster is defined in the Envoy
// bootstrap instead of CDS (the zone-aware local cluster). Those are requested
// by name by the proxy but have no CDS cluster to match, so they are kept
// rather than filtered; they are never synthesized, since a name only reaches
// this set when its CLA is present.
func filterEndpointResourcesForClusters(
	clusters envoycache.Resources,
	endpoints envoycache.Resources,
	bootstrapEndpointNames map[string]struct{},
) envoycache.Resources {
	requiredEndpointNames := make(map[string]struct{}, len(bootstrapEndpointNames))
	for name := range bootstrapEndpointNames {
		requiredEndpointNames[name] = struct{}{}
	}
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
	// Synthesize empty assignments for EDS clusters that have no CLA yet so the
	// published snapshot stays EDS-consistent.
	synthesized := 0
	for name := range requiredEndpointNames {
		if _, ok := covered[name]; ok {
			continue
		}
		empty := &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}
		filteredEndpoints = append(filteredEndpoints, envoycachetypes.ResourceWithTTL{Resource: empty})
		resourcesHash ^= utils.HashProto(empty)
		synthesized++
	}
	if synthesized == 0 && len(filteredEndpoints) == len(endpoints.Items) {
		return endpoints
	}
	return envoycache.NewResourcesWithTTL(strconv.FormatUint(resourcesHash, 10), filteredEndpoints)
}
