package setup

import (
	"slices"
	"sync/atomic"

	"istio.io/istio/pkg/kube/krt"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/extensions2/plugins/destrule"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// localityGate answers, at stream-connect time, whether locality-aware routing
// is in use AND the topology actually spans the dimension that routing keys on
// — i.e. whether folding pod locality into a client's xDS identity can change
// any client's config. It backs the PodLocalityXDS "AUTO" mode: when locality
// cannot affect the result, the identity is collapsed to one per gateway role.
//
// Endpoint spread is a sound signal: if a backend's endpoints do not span the
// keyed dimension, every proxy computes the same prioritization for it (one
// priority tier, all endpoints active), so collapsing is behaviorally neutral.
type localityGate struct {
	endpoints krt.Collection[ir.EndpointsForBackend]
	// drs is nil when Istio integration is disabled, since DestinationRules are
	// not processed and their localityLbSetting cannot affect routing.
	drs krt.Collection[destrule.DestinationRuleWrapper]
}

// synced reports whether the gate's source collections have completed their
// initial sync. Until then callers assume locality is in use, so a cluster
// that genuinely relies on it never prematurely collapses identities.
func (g *localityGate) synced() bool {
	if !g.endpoints.HasSynced() {
		return false
	}
	if g.drs != nil && !g.drs.HasSynced() {
		return false
	}
	return true
}

// localityInUse reads the live collection snapshots (per stream connect, which
// is infrequent) and applies the topology-aware rule.
func (g *localityGate) localityInUse() bool {
	var drs []destrule.DestinationRuleWrapper
	if g.drs != nil {
		drs = g.drs.List()
	}
	return localityInUseFromInputs(g.endpoints.List(), drs)
}

// localityInUseFromInputs is the pure decision: locality can affect routing iff
// some backend uses a traffic distribution (or a DestinationRule sets an
// enabled localityLbSetting) AND its endpoints actually span the keyed locality
// dimension.
//
//   - PreferSameZone keys on region/zone: matters only if endpoints span >1
//     distinct (region, zone).
//   - DestinationRule localityLbSetting can prioritize down to subzone: matters
//     only if endpoints span >1 distinct (region, zone, subzone).
//   - PreferSameNode (node/hostname) and PreferNetwork key on dimensions not
//     captured by PodLocality, so we cannot prove them inert here and
//     conservatively keep locality on.
func localityInUseFromInputs(eps []ir.EndpointsForBackend, drs []destrule.DestinationRuleWrapper) bool {
	for _, e := range eps {
		switch e.TrafficDistribution {
		case wellknown.TrafficDistributionPreferSameNode, wellknown.TrafficDistributionPreferNetwork:
			return true
		case wellknown.TrafficDistributionPreferSameZone:
			if localitiesSpanZone(e.LbEps) {
				return true
			}
		}
	}
	if len(drs) > 0 && slices.ContainsFunc(drs, destrule.HasEnabledLocalityLbSetting) {
		for _, e := range eps {
			if localitiesSpanSubzone(e.LbEps) {
				return true
			}
		}
	}
	return false
}

// localitiesSpanZone reports whether the endpoint localities cover more than one
// distinct (region, zone).
func localitiesSpanZone(m ir.LocalityLbMap) bool {
	var first ir.PodLocality
	seen := false
	for loc := range m {
		rz := ir.PodLocality{Region: loc.Region, Zone: loc.Zone}
		if !seen {
			first = rz
			seen = true
			continue
		}
		if rz != first {
			return true
		}
	}
	return false
}

// localitiesSpanSubzone reports whether the endpoint localities cover more than
// one distinct (region, zone, subzone). The LbEps map is keyed by the full
// PodLocality, so more than one key means the localities differ at subzone
// granularity or coarser.
func localitiesSpanSubzone(m ir.LocalityLbMap) bool {
	return len(m) > 1
}

// newLocalityGate builds a localityGate from the (already plugin-initialized)
// endpoints collection and, when Istio integration is enabled, a DestinationRule
// collection. It must be called after InitPlugins has populated the endpoints
// collection. The DestinationRule informer shares the istio client's per-GVR
// informer with the destrule plugin, so this does not double-watch the API.
func newLocalityGate(
	krtOpts krtutil.KrtOptions,
	endpoints krt.Collection[ir.EndpointsForBackend],
	istioClient apiclient.Client,
	istioEnabled bool,
) *localityGate {
	g := &localityGate{endpoints: endpoints}
	if istioEnabled {
		idx := destrule.NewDestRuleIndex(istioClient, &krtOpts)
		g.drs = idx.Destrules
	}
	return g
}

// podLocalityDecision resolves, per stream connect, whether pod locality should
// be folded into the client identity, according to the PodLocalityXDS setting.
// For AUTO it consults a late-bound localityGate (installed once the endpoints
// collection is ready); the atomic makes that late binding safe to read from the
// xDS callback goroutines.
type podLocalityDecision struct {
	mode apisettings.PodLocalityXDS
	gate atomic.Pointer[localityGate]
}

func newPodLocalityDecision(mode apisettings.PodLocalityXDS) *podLocalityDecision {
	return &podLocalityDecision{mode: mode}
}

// use is the func() bool consulted by the UCC builder.
func (d *podLocalityDecision) use() bool {
	switch d.mode {
	case apisettings.PodLocalityXDSOff:
		return false
	case apisettings.PodLocalityXDSOn:
		return true
	default: // AUTO
		g := d.gate.Load()
		if g == nil || !g.synced() {
			// Not built or not yet synced: assume locality is in use so a
			// cluster that relies on it is never prematurely collapsed.
			return true
		}
		return g.localityInUse()
	}
}

// install binds the locality gate for AUTO mode. ON/OFF never read the gate.
func (d *podLocalityDecision) install(g *localityGate) {
	d.gate.Store(g)
}
