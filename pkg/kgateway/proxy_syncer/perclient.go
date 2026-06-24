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
	uccCol krt.Collection[ir.UniqlyConnectedClient],
	mostXdsSnapshots krt.Collection[GatewayXdsResources],
	endpoints PerClientEnvoyEndpoints,
	clusters PerClientEnvoyClusters,
) krt.Collection[XdsSnapWrapper] {
	clusterSnapshot := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) *clustersWithErrors {
		clustersForUcc := clusters.FetchClustersForClient(kctx, ucc)
		if len(clustersForUcc) == 0 {
			logger.Info("no perclient clusters; publishing snapshot with empty per-client cluster set", "client", ucc.ResourceName())
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
		endpointsForUcc := endpoints.FetchEndpointsForClient(kctx, ucc)
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

	xdsSnapshotsForUcc := krt.NewCollection(uccCol, func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) *XdsSnapWrapper {
		defer (collectXDSTransformMetrics(ucc.ResourceName()))(nil)

		listenerRouteSnapshot := krt.FetchOne(kctx, mostXdsSnapshots, krt.FilterKey(ucc.Role))
		if listenerRouteSnapshot == nil {
			logger.Debug("snapshot missing", "proxy_key", ucc.Role)
			return nil
		}
		clustersForUcc := krt.FetchOne(kctx, clusterSnapshot, krt.FilterKey(ucc.ResourceName()))
		clientEndpointResources := krt.FetchOne(kctx, endpointResources, krt.FilterKey(ucc.ResourceName()))

		// Publish best-effort per-client snapshots even while the per-client
		// collections are catching up. Returning nil for these readiness gaps
		// withdraws the KRT output row, and the xDS subscriber intentionally
		// treats that Delete as a no-op so Envoy retains its previous snapshot.
		// That "wait for completeness" behavior can strand a client on stale
		// endpoints forever if a recompute edge is missed or an input remains
		// temporarily incomplete. We keep the reference scans below for logs and
		// metrics, but they are diagnostics only; publication continues.
		//
		// BackendRef typos never reach these diagnostics as real cluster names:
		// IR-time resolution substitutes wellknown.BlackholeClusterName, which
		// findMissingReferencedClusters explicitly skips.
		if clustersForUcc == nil {
			clustersForUcc = &clustersWithErrors{
				clusters: envoycache.Resources{
					Items: map[string]envoycachetypes.ResourceWithTTL{},
				},
			}
		}
		if clientEndpointResources == nil {
			logger.Info("per-client endpoints not ready; publishing snapshot with empty endpoint resources", "client", ucc.ResourceName())
			recordIncompleteXDSPublish(ucc.ResourceName(), "endpoints_not_ready")
			clientEndpointResources = &endpointsWithUccName{
				endpoints:    envoycache.NewResourcesWithTTL("0", nil),
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
			logger.Warn(
				"publishing snapshot before all referenced clusters are ready",
				"client", ucc.ResourceName(),
				"missing_clusters", missingClusters,
			)
			recordIncompleteXDSPublish(ucc.ResourceName(), "missing_clusters")
		}
		// Publish only CLAs required by EDS clusters in this CDS snapshot so ADS
		// does not retain stale endpoint resources Envoy will no longer request.
		endpointRes := filterEndpointResourcesForClusters(clusterResources, clientEndpointResources.endpoints)
		if missingEndpointClusters := findMissingReferencedEndpointResources(
			listenerRouteSnapshot.ReferencedClusters,
			clusterResources.Items,
			endpointRes.Items,
			clustersForUcc.erroredClusters,
		); len(missingEndpointClusters) > 0 {
			logger.Warn(
				"publishing snapshot before all referenced EDS resources are ready",
				"client", ucc.ResourceName(),
				"missing_endpoint_clusters", missingEndpointClusters,
			)
			recordIncompleteXDSPublish(ucc.ResourceName(), "missing_endpoints")
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
		if _, ok := endpoints[endpointResourceName]; ok {
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

// filterEndpointResourcesForClusters returns endpoint resources for EDS clusters present in
// the same CDS snapshot. Envoy will not request CLAs for STATIC clusters or clusters no longer
// present in CDS; leaving those stale CLAs in the ADS snapshot can make the cache refuse named
// EDS responses.
func filterEndpointResourcesForClusters(clusters envoycache.Resources, endpoints envoycache.Resources) envoycache.Resources {
	requiredEndpointNames := make(map[string]struct{})
	for _, item := range clusters.Items {
		endpointName, requiresEndpoint := endpointResourceNameForCluster(item)
		if requiresEndpoint {
			requiredEndpointNames[endpointName] = struct{}{}
		}
	}

	filteredEndpoints := make([]envoycachetypes.ResourceWithTTL, 0, len(endpoints.Items))
	changed := false
	for _, item := range endpoints.Items {
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		if !ok {
			changed = true
			continue
		}
		if _, required := requiredEndpointNames[cla.GetClusterName()]; !required {
			changed = true
			continue
		}
		filteredEndpoints = append(filteredEndpoints, item)
	}
	if !changed && len(filteredEndpoints) == len(endpoints.Items) {
		return endpoints
	}
	return envoycache.NewResourcesWithTTL(filteredEndpointResourcesVersion(endpoints.Version, filteredEndpoints), filteredEndpoints)
}

func filteredEndpointResourcesVersion(baseVersion string, endpoints []envoycachetypes.ResourceWithTTL) string {
	var namesHash uint64
	for _, name := range sortedResourceNames(endpoints) {
		namesHash ^= utils.HashString(name)
	}
	return fmt.Sprintf("%s/%d", baseVersion, namesHash)
}

func sortedResourceNames(resources []envoycachetypes.ResourceWithTTL) []string {
	names := make([]string, 0, len(resources))
	for _, item := range resources {
		name := envoycache.GetResourceName(item.Resource)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
