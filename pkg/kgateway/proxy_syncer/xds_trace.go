package proxy_syncer

import (
	"cmp"
	"slices"
	"strconv"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

// XdsSnapshotTraceEvent records one snapshotPerClient decision — a defer or
// a publish — together with the snapshot data the decision was made on. The
// formal-methods trace conformance harness (devel/formal/lean) replays these
// events against the verified xDS publication spec; see the
// `xdsspec trace` command and devel/formal/lean/README.md.
type XdsSnapshotTraceEvent struct {
	Schema   int    `json:"schema"`
	Scenario string `json:"scenario"`
	Sequence uint64 `json:"sequence"`
	Client   string `json:"client"`
	Decision string `json:"decision"`
	// ReferencedClusters are the dataplane routing targets collected from
	// LDS/RDS.
	ReferencedClusters []string `json:"referenced"`
	// ExemptClusters are referenced names the publication gate deliberately
	// skips: errored clusters and the blackhole sentinel.
	ExemptClusters []string                   `json:"exempt"`
	Clusters       []XdsSnapshotTraceCluster  `json:"clusters"`
	Endpoints      []XdsSnapshotTraceEndpoint `json:"endpoints"`
	// EndpointsVersion is the version string of the (filtered) EDS resource
	// set that would be published.
	EndpointsVersion string `json:"endpointsVersion"`
	// Versions holds every resource type's version string in the snapshot the
	// decision or installation refers to, keyed cluster, endpoint, listener,
	// route, secret. Empty when no snapshot was built (early defers). The
	// checker matches installations to decisions on the whole tuple.
	Versions map[string]string `json:"versions"`
}

type XdsSnapshotTraceCluster struct {
	Name string `json:"name"`
	EDS  bool   `json:"eds"`
	// EDSName is the ClusterLoadAssignment name this cluster's named EDS
	// request will use (service_name when set, else the cluster name).
	EDSName string `json:"edsName"`
}

type XdsSnapshotTraceEndpoint struct {
	Name   string `json:"name"`
	Usable bool   `json:"usable"`
	// Digest is the decimal form of the same per-resource content hash that
	// filterEndpointResourcesForClusters XORs into the EDS version. It lets
	// the trace checker relate consecutive publications for one client:
	// an unchanged version must carry unchanged content (RF-008). The
	// digest exposes no endpoint addresses.
	Digest string `json:"digest"`
}

// xdsTraceSchema is the snapshot trace schema version. Schema 2 added the
// per-endpoint content digest; the checker rejects other schemas.
const xdsTraceSchema = 2

// Decisions recorded by the trace hook. The trace conformance checker
// (devel/formal/lean/XdsSpec/TraceCheck.lean) validates the invariants on
// every publication decision. "publish" is
// emitted where the published content is decided: in snapshotPerClient for
// coherent snapshots, and in syncXds for the per-cluster resolutions of
// deferred ones (carry-forward / held-flip compositions).
const (
	xdsTraceDecisionPublish = "publish"
	// A first cache publication with complete CDS and possibly empty CLAs.
	// This identifies cache state, not whether the connecting proxy is new.
	xdsTraceDecisionPublishFirst = "publish-first"
	// A per-cluster resolution published by syncXds (held flip, carried
	// clusters, or a scale-to-zero truth publish). Checked like a publish
	// except that a referenced cluster's CLA may legitimately be empty:
	// previously-referenced clusters publish their truth (spec case C2).
	xdsTraceDecisionPublishResolved        = "publish-resolved"
	xdsTraceDecisionDeferRoleSnapshot      = "defer-missing-role-snapshot"
	xdsTraceDecisionDeferEndpointsNotReady = "defer-endpoints-not-ready"
	// The transform built a snapshot with unready referenced clusters;
	// syncXds resolves it per cluster.
	xdsTraceDecisionDeferFlip = "defer-flip"
	// No cached snapshot and a nonexempt referenced cluster missing from CDS.
	xdsTraceDecisionDeferFirstPublish = "defer-first-publish"
	// Cache installation receipts emitted by syncXds after SetSnapshot returns.
	// They carry the installed snapshot, so the checker can relate the decided
	// content to the content the cache serves (RF-006, first lifecycle stage).
	xdsTraceDecisionInstalled     = "installed"
	xdsTraceDecisionInstallFailed = "install-failed"
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
	snap *envoycache.Snapshot,
) {
	if xdsSnapshotTraceSink == nil {
		return
	}

	event := XdsSnapshotTraceEvent{
		Client:             client,
		Decision:           decision,
		ReferencedClusters: []string{}, ExemptClusters: []string{},
		Clusters: []XdsSnapshotTraceCluster{}, Endpoints: []XdsSnapshotTraceEndpoint{},
	}
	for name := range referencedClusters {
		event.ReferencedClusters = append(event.ReferencedClusters, name)
	}
	slices.Sort(event.ReferencedClusters)
	event.ExemptClusters = append(event.ExemptClusters, erroredClusters...)
	event.ExemptClusters = append(event.ExemptClusters, wellknown.BlackholeClusterName)
	slices.Sort(event.ExemptClusters)
	for name, item := range clusters.Items {
		edsName, isEDS := endpointResourceNameForCluster(item)
		event.Clusters = append(event.Clusters, XdsSnapshotTraceCluster{
			Name:    name,
			EDS:     isEDS,
			EDSName: edsName,
		})
	}
	slices.SortFunc(event.Clusters, func(a, b XdsSnapshotTraceCluster) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, item := range endpoints.Items {
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		if !ok {
			continue
		}
		event.Endpoints = append(event.Endpoints, XdsSnapshotTraceEndpoint{
			Name:   cla.GetClusterName(),
			Usable: clusterLoadAssignmentHasUsableEndpoint(item),
			Digest: strconv.FormatUint(utils.HashProto(cla), 10),
		})
	}
	slices.SortFunc(event.Endpoints, func(a, b XdsSnapshotTraceEndpoint) int {
		return cmp.Compare(a.Name, b.Name)
	})
	event.EndpointsVersion = endpoints.Version
	event.Versions = snapshotTypeVersions(snap)

	xdsSnapshotTraceSink(event)
}

// snapshotTypeVersions projects a snapshot's per-type version strings for the
// trace. A nil snapshot yields an empty, non-nil map so the field is always
// present in the emitted JSON.
func snapshotTypeVersions(snap *envoycache.Snapshot) map[string]string {
	versions := map[string]string{}
	if snap == nil {
		return versions
	}
	for name, rt := range map[string]envoycachetypes.ResponseType{
		"cluster":  envoycachetypes.Cluster,
		"endpoint": envoycachetypes.Endpoint,
		"listener": envoycachetypes.Listener,
		"route":    envoycachetypes.Route,
		"secret":   envoycachetypes.Secret,
	} {
		versions[name] = snap.Resources[rt].Version
	}
	return versions
}
