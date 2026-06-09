package proxy_syncer

import (
	"sort"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

// XdsSnapshotTraceEvent records one snapshotPerClient decision — a defer or
// a publish — together with the snapshot data the decision was made on. The
// formal-methods trace conformance harness (devel/formal/lean) replays these
// events against the verified xDS publication spec; see the
// `xdsspec trace` command and devel/formal/lean/README.md.
type XdsSnapshotTraceEvent struct {
	Client   string `json:"client"`
	Decision string `json:"decision"`
	// ReferencedClusters are the dataplane routing targets collected from
	// LDS/RDS.
	ReferencedClusters []string `json:"referenced,omitempty"`
	// ExemptClusters are referenced names the publication gate deliberately
	// skips: errored clusters and the blackhole sentinel.
	ExemptClusters []string                   `json:"exempt,omitempty"`
	Clusters       []XdsSnapshotTraceCluster  `json:"clusters,omitempty"`
	Endpoints      []XdsSnapshotTraceEndpoint `json:"endpoints,omitempty"`
	// EndpointsVersion is the version string of the (filtered) EDS resource
	// set that would be published.
	EndpointsVersion string `json:"endpointsVersion,omitempty"`
}

type XdsSnapshotTraceCluster struct {
	Name string `json:"name"`
	EDS  bool   `json:"eds"`
	// EDSName is the ClusterLoadAssignment name this cluster's named EDS
	// request will use (service_name when set, else the cluster name).
	EDSName string `json:"edsName,omitempty"`
}

type XdsSnapshotTraceEndpoint struct {
	Name   string `json:"name"`
	Usable bool   `json:"usable"`
}

// Decisions recorded by the trace hook, mirroring the guards documented in
// snapshotPerClient.
const (
	xdsTraceDecisionPublish                = "publish"
	xdsTraceDecisionDeferRoleSnapshot      = "defer-missing-role-snapshot"
	xdsTraceDecisionDeferEndpointsNotReady = "defer-endpoints-not-ready"
	xdsTraceDecisionDeferMissingClusters   = "defer-missing-clusters"
	xdsTraceDecisionDeferMissingEndpoints  = "defer-missing-endpoint-clusters"
)

// xdsSnapshotTraceSink, when non-nil, observes every snapshotPerClient
// decision. It is nil in production (the only cost is a nil check) and is
// installed by the trace conformance test harness via XDS_TRACE_OUT.
var xdsSnapshotTraceSink func(XdsSnapshotTraceEvent)

func emitXdsSnapshotTrace(
	client string,
	decision string,
	referencedClusters map[string]struct{},
	erroredClusters []string,
	clusters envoycache.Resources,
	endpoints envoycache.Resources,
) {
	if xdsSnapshotTraceSink == nil {
		return
	}

	event := XdsSnapshotTraceEvent{
		Client:   client,
		Decision: decision,
	}
	for name := range referencedClusters {
		event.ReferencedClusters = append(event.ReferencedClusters, name)
	}
	sort.Strings(event.ReferencedClusters)
	event.ExemptClusters = append(event.ExemptClusters, erroredClusters...)
	event.ExemptClusters = append(event.ExemptClusters, wellknown.BlackholeClusterName)
	sort.Strings(event.ExemptClusters)
	for name, item := range clusters.Items {
		edsName, isEDS := endpointResourceNameForCluster(item)
		event.Clusters = append(event.Clusters, XdsSnapshotTraceCluster{
			Name:    name,
			EDS:     isEDS,
			EDSName: edsName,
		})
	}
	sort.Slice(event.Clusters, func(i, j int) bool {
		return event.Clusters[i].Name < event.Clusters[j].Name
	})
	for _, item := range endpoints.Items {
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		if !ok {
			continue
		}
		event.Endpoints = append(event.Endpoints, XdsSnapshotTraceEndpoint{
			Name:   cla.GetClusterName(),
			Usable: clusterLoadAssignmentHasUsableEndpoint(item),
		})
	}
	sort.Slice(event.Endpoints, func(i, j int) bool {
		return event.Endpoints[i].Name < event.Endpoints[j].Name
	})
	event.EndpointsVersion = endpoints.Version

	xdsSnapshotTraceSink(event)
}
