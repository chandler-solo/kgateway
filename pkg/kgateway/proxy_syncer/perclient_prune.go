package proxy_syncer

import (
	"fmt"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytcpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

// This file implements R3 and S4 of
// devel/architecture/perclient-xds-publication.md.
//
// R3: when a route or TCP filter chain targets a cluster that is absent from
// the per-client inputs AND was never previously published (so there is
// nothing to carry forward), the referencing route entry or filter chain — and
// only it — is held at its previously published version, or omitted if it
// never had one. Everything else publishes, preserving reference closure (S1)
// without withholding the whole snapshot.
//
// S4 (route-transition readiness, make-before-break at route granularity): a
// route entry or TCP filter chain whose target set CHANGED relative to the
// previously published version — a new entry, or a retarget — activates only
// once every new target is usable (non-EDS, or has a CLA with at least one
// non-UNHEALTHY endpoint). Until then the entry is held at its previous form,
// or withheld if new. Entries whose targets did not change are never held:
// endpoint truth (S3) flows to them unconditionally, so a backend scaling to
// zero propagates as an empty CLA instead of pinning stale endpoints.

var tcpProxyTypeURL = "type.googleapis.com/" + string(proto.MessageName(&envoytcpv3.TcpProxy{}))

// routeEntryClusterRefs returns the dataplane cluster references of a single
// route entry (direct cluster or weighted clusters), mirroring the scope of
// collectReferencedClusters for RDS.
func routeEntryClusterRefs(r *envoyroutev3.Route) []string {
	action := r.GetRoute()
	if action == nil {
		return nil
	}
	switch specifier := action.GetClusterSpecifier().(type) {
	case *envoyroutev3.RouteAction_Cluster:
		if specifier.Cluster != "" {
			return []string{specifier.Cluster}
		}
	case *envoyroutev3.RouteAction_WeightedClusters:
		clusters := specifier.WeightedClusters.GetClusters()
		refs := make([]string, 0, len(clusters))
		for _, c := range clusters {
			if c.GetName() != "" {
				refs = append(refs, c.GetName())
			}
		}
		return refs
	}
	return nil
}

// tcpFilterChainClusterRefs returns the dataplane cluster references of a TCP
// proxy filter chain, mirroring the scope of collectReferencedClusters for LDS.
func tcpFilterChainClusterRefs(chain *envoylistenerv3.FilterChain) []string {
	var refs []string
	for _, f := range chain.GetFilters() {
		typedConfig := f.GetTypedConfig()
		if typedConfig == nil || typedConfig.GetTypeUrl() != tcpProxyTypeURL {
			continue
		}
		var tcpProxy envoytcpv3.TcpProxy
		if err := typedConfig.UnmarshalTo(&tcpProxy); err != nil {
			continue
		}
		switch specifier := tcpProxy.GetClusterSpecifier().(type) {
		case *envoytcpv3.TcpProxy_Cluster:
			if specifier.Cluster != "" {
				refs = append(refs, specifier.Cluster)
			}
		case *envoytcpv3.TcpProxy_WeightedClusters:
			for _, c := range specifier.WeightedClusters.GetClusters() {
				if c.GetName() != "" {
					refs = append(refs, c.GetName())
				}
			}
		}
	}
	return refs
}

func referencesAny(refs []string, set map[string]struct{}) bool {
	for _, r := range refs {
		if _, ok := set[r]; ok {
			return true
		}
	}
	return false
}

// clusterLoadAssignmentHasUsableEndpoint reports whether the CLA contains at
// least one endpoint Envoy could route to (not explicitly UNHEALTHY). This
// mirrors Envoy warming semantics: a cluster without a usable endpoint serves
// only 503s.
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

// clustersWithoutUsableEndpoints returns the EDS clusters in clusters whose CLA
// is absent, malformed, empty, or contains only unhealthy endpoints — the
// "not usable" set S4 gates route transitions on.
func clustersWithoutUsableEndpoints(
	clusters map[string]envoycachetypes.ResourceWithTTL,
	endpoints map[string]envoycachetypes.ResourceWithTTL,
) map[string]struct{} {
	var notUsable map[string]struct{}
	for name, item := range clusters {
		claName, isEDS := endpointResourceNameForCluster(item)
		if !isEDS {
			continue
		}
		cla, ok := endpoints[claName]
		if ok && clusterLoadAssignmentHasUsableEndpoint(cla) {
			continue
		}
		if notUsable == nil {
			notUsable = map[string]struct{}{}
		}
		notUsable[name] = struct{}{}
	}
	return notUsable
}

func equalRefSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, r := range a {
		set[r] = struct{}{}
	}
	for _, r := range b {
		if _, ok := set[r]; !ok {
			return false
		}
	}
	return true
}

// pruneSets carries the two reasons an entry may be withheld, plus whether
// transition checking (S4) is active. checkTransitions is false on controller
// cold start (no previously published snapshot to define "changed"; Envoy's
// own warming covers that window) and when the route/listener resources are
// version-identical to the previously published ones.
type pruneSets struct {
	unsatisfied      map[string]struct{}
	notUsable        map[string]struct{}
	checkTransitions bool
}

// entryNeedsIntervention decides whether an entry with the given current refs
// and previous entry refs (prevRefs is nil when no previous entry exists) must
// be held or omitted: it references an unsatisfiable cluster (R3), or it is a
// transition — new entry, or target set changed — to a not-yet-usable cluster
// (S4).
func (p pruneSets) entryNeedsIntervention(refs []string, prevRefs []string, hasPrev bool) bool {
	if referencesAny(refs, p.unsatisfied) {
		return true
	}
	if !p.checkTransitions || !referencesAny(refs, p.notUsable) {
		return false
	}
	isTransition := !hasPrev || !equalRefSets(refs, prevRefs)
	return isTransition
}

// pruneRouteConfigurations rewrites RDS resources so no remaining route entry
// references an unsatisfied cluster (R3) and, when transition checking is
// active, no changed/new entry targets a not-yet-usable cluster (S4). Each
// intervened entry is replaced by its previously published version (matched by
// RouteConfiguration name, virtual host name, and route name) when that
// version is itself satisfiable, else removed. Returns the (possibly
// unchanged) resources and held/omitted counts.
func pruneRouteConfigurations(
	routes envoycache.Resources,
	prevRoutes map[string]envoycachetypes.ResourceWithTTL,
	sets pruneSets,
) (envoycache.Resources, int, int) {
	held, omitted := 0, 0
	outItems := make([]envoycachetypes.ResourceWithTTL, 0, len(routes.Items))
	for name, item := range routes.Items {
		rc, ok := item.Resource.(*envoyroutev3.RouteConfiguration)
		if !ok {
			outItems = append(outItems, item)
			continue
		}
		var prevRC *envoyroutev3.RouteConfiguration
		if prevItem, ok := prevRoutes[name]; ok {
			prevRC, _ = prevItem.Resource.(*envoyroutev3.RouteConfiguration)
		}
		clone := proto.Clone(rc).(*envoyroutev3.RouteConfiguration)
		rcHeld, rcOmitted := 0, 0
		for _, vh := range clone.GetVirtualHosts() {
			kept := make([]*envoyroutev3.Route, 0, len(vh.GetRoutes()))
			for _, r := range vh.GetRoutes() {
				refs := routeEntryClusterRefs(r)
				prevEntry := findPreviousRouteEntry(prevRC, vh.GetName(), r.GetName())
				var prevRefs []string
				if prevEntry != nil {
					prevRefs = routeEntryClusterRefs(prevEntry)
				}
				if !sets.entryNeedsIntervention(refs, prevRefs, prevEntry != nil) {
					kept = append(kept, r)
					continue
				}
				// Hold at the previous version when one exists and is itself
				// publishable. The previous entry MAY target a now-unusable
				// cluster — that is what Envoy is already serving, so keeping it
				// is a no-op for the dataplane.
				if prevEntry != nil && !referencesAny(prevRefs, sets.unsatisfied) {
					kept = append(kept, prevEntry)
					rcHeld++
					continue
				}
				rcOmitted++
			}
			vh.Routes = kept
		}
		if rcHeld+rcOmitted == 0 {
			outItems = append(outItems, item)
			continue
		}
		held += rcHeld
		omitted += rcOmitted
		outItems = append(outItems, envoycachetypes.ResourceWithTTL{Resource: clone, TTL: item.TTL})
	}
	if held+omitted == 0 {
		return routes, 0, 0
	}
	return resourcesWithRecomputedVersion(outItems), held, omitted
}

// findPreviousRouteEntry locates a route entry by virtual host and route name in
// a previously published RouteConfiguration. Unnamed routes cannot be matched
// and are treated as not found (omitted rather than held).
func findPreviousRouteEntry(rc *envoyroutev3.RouteConfiguration, vhName, routeName string) *envoyroutev3.Route {
	if rc == nil || routeName == "" {
		return nil
	}
	for _, vh := range rc.GetVirtualHosts() {
		if vh.GetName() != vhName {
			continue
		}
		for _, r := range vh.GetRoutes() {
			if r.GetName() == routeName {
				return r
			}
		}
	}
	return nil
}

// pruneListeners rewrites LDS resources so no remaining TCP proxy filter chain
// references an unsatisfied cluster (R3) and, when transition checking is
// active, no changed/new chain targets a not-yet-usable cluster (S4). Chains
// are held at their previously published version (matched by listener name and
// filter chain name) or omitted.
func pruneListeners(
	listeners envoycache.Resources,
	prevListeners map[string]envoycachetypes.ResourceWithTTL,
	sets pruneSets,
) (envoycache.Resources, int, int) {
	held, omitted := 0, 0
	outItems := make([]envoycachetypes.ResourceWithTTL, 0, len(listeners.Items))
	for name, item := range listeners.Items {
		l, ok := item.Resource.(*envoylistenerv3.Listener)
		if !ok {
			outItems = append(outItems, item)
			continue
		}
		var prevListener *envoylistenerv3.Listener
		if prevItem, ok := prevListeners[name]; ok {
			prevListener, _ = prevItem.Resource.(*envoylistenerv3.Listener)
		}
		clone := proto.Clone(l).(*envoylistenerv3.Listener)
		lHeld, lOmitted := 0, 0
		kept := make([]*envoylistenerv3.FilterChain, 0, len(clone.GetFilterChains()))
		for _, chain := range clone.GetFilterChains() {
			refs := tcpFilterChainClusterRefs(chain)
			prevChain := findPreviousFilterChain(prevListener, chain.GetName())
			var prevRefs []string
			if prevChain != nil {
				prevRefs = tcpFilterChainClusterRefs(prevChain)
			}
			if !sets.entryNeedsIntervention(refs, prevRefs, prevChain != nil) {
				kept = append(kept, chain)
				continue
			}
			if prevChain != nil && !referencesAny(prevRefs, sets.unsatisfied) {
				kept = append(kept, prevChain)
				lHeld++
				continue
			}
			lOmitted++
		}
		clone.FilterChains = kept
		if dfc := clone.GetDefaultFilterChain(); dfc != nil {
			refs := tcpFilterChainClusterRefs(dfc)
			prevChain := prevDefaultFilterChain(prevListener)
			var prevRefs []string
			if prevChain != nil {
				prevRefs = tcpFilterChainClusterRefs(prevChain)
			}
			if sets.entryNeedsIntervention(refs, prevRefs, prevChain != nil) {
				if prevChain != nil && !referencesAny(prevRefs, sets.unsatisfied) {
					clone.DefaultFilterChain = prevChain
					lHeld++
				} else {
					clone.DefaultFilterChain = nil
					lOmitted++
				}
			}
		}
		if lHeld+lOmitted == 0 {
			outItems = append(outItems, item)
			continue
		}
		held += lHeld
		omitted += lOmitted
		outItems = append(outItems, envoycachetypes.ResourceWithTTL{Resource: clone, TTL: item.TTL})
	}
	if held+omitted == 0 {
		return listeners, 0, 0
	}
	return resourcesWithRecomputedVersion(outItems), held, omitted
}

// findPreviousFilterChain locates a filter chain by name in a previously
// published Listener. Unnamed chains cannot be matched.
func findPreviousFilterChain(l *envoylistenerv3.Listener, chainName string) *envoylistenerv3.FilterChain {
	if l == nil || chainName == "" {
		return nil
	}
	for _, chain := range l.GetFilterChains() {
		if chain.GetName() == chainName {
			return chain
		}
	}
	return nil
}

func prevDefaultFilterChain(l *envoylistenerv3.Listener) *envoylistenerv3.FilterChain {
	if l == nil {
		return nil
	}
	return l.GetDefaultFilterChain()
}

// resourcesWithRecomputedVersion builds an envoycache.Resources whose version is
// the XOR of the content hashes of its items, matching how upstream versions
// are derived so identical contents always share a version.
func resourcesWithRecomputedVersion(items []envoycachetypes.ResourceWithTTL) envoycache.Resources {
	var hash uint64
	for _, item := range items {
		hash ^= utils.HashProto(item.Resource)
	}
	return envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", hash), items)
}
