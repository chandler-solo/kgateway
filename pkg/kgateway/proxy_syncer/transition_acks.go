package proxy_syncer

import (
	"slices"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyrawbufferv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/raw_buffer/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

// transitionAcks reports what each of a client's xDS streams has accepted, so
// the transition windows can end when the proxy has the update rather than after
// a fixed time (KGW_CLUSTER_TRANSITION_ACKS). The control plane's xDS cache
// implements it.
type transitionAcks interface {
	// AckedResources reports whether every stream of client that has been sent
	// a typeURL response last ACKed one carrying every name in names, and some
	// stream has been sent one. Cluster and endpoint types are tracked by name.
	AckedResources(client, typeURL string, names []string) bool
	// AckedVersions returns, for each stream of client that has been sent a
	// typeURL response, the version it last ACKed, or "" if none.
	AckedVersions(client, typeURL string) map[int64]string
	// NotifyAcks runs fn after a stream ACKs a response or closes. fn must not
	// block.
	NotifyAcks(fn func(client string))
}

// installRecord is a snapshot installed for a client: its versions, numbered in
// install order.
type installRecord struct {
	seq                                    uint64
	clusters, endpoints, routes, listeners string
}

// maxInstallRecords bounds a client's install history. A window whose opening
// install falls out of it cannot be confirmed by ACKs any more, and closes at
// its duration instead.
const maxInstallRecords = 64

// useTransitionAcks ends transition windows on the proxy's ACKs from now on.
func (g *publishGate) useTransitionAcks(acks transitionAcks) {
	g.acks = acks
	acks.NotifyAcks(g.acksChanged)
}

// acksChanged runs on a stream's goroutine after it ACKs or closes. Only a
// client with a window an ACK could end is checked, on its own goroutine, so a
// stream never waits on the gate.
func (g *publishGate) acksChanged(proxyKey string) {
	if _, waiting := g.awaitingAcks.Load(proxyKey); waiting {
		go g.checkTransitionAcks(proxyKey)
	}
}

// awaitAcksLocked marks proxyKey as having a window an ACK could end. Callers
// must hold g.mu.
func (g *publishGate) awaitAcksLocked(proxyKey string) {
	if g.acks != nil {
		g.awaitingAcks.Store(proxyKey, struct{}{})
	}
}

// checkTransitionAcks ends proxyKey's windows that every connected stream has
// acknowledged: a reference-ahead hold whose new clusters every stream has,
// and de-reference windows whose route update every stream has applied. A
// window no ACK ends still closes at its duration.
func (g *publishGate) checkTransitionAcks(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.acks == nil {
		return
	}
	if pf := g.pendingFlips[proxyKey]; pf != nil && pf.releaseNow != nil && len(pf.blocking) == 0 && len(pf.ahead) > 0 &&
		g.aheadAckedLocked(proxyKey, pf) {
		logger.Debug("proxy acknowledged the clusters a reference-ahead hold waits for; releasing it",
			"proxy_key", proxyKey, "reference_ahead", pf.ahead)
		recordClusterScopingTransition(proxyKey, transitionReferenceAheadAcked, len(pf.ahead))
		// The proxy has the clusters: the budget did not cut the window short.
		pf.capped = false
		pf.releaseNow()
	}
	if state := g.dereferenced[proxyKey]; state != nil && state.pruneNow != nil {
		if acked := g.dereferencesAckedLocked(proxyKey, state); len(acked) > 0 {
			if state.acked == nil {
				state.acked = make(map[string]struct{}, len(acked))
			}
			for _, name := range acked {
				state.acked[name] = struct{}{}
			}
			logger.Debug("proxy applied the route update that stopped naming de-referenced clusters; removing them",
				"proxy_key", proxyKey, "clusters", acked)
			recordClusterScopingTransition(proxyKey, transitionDereferenceAcked, len(acked))
			state.pruneNow()
		}
	}
	if !g.awaitsAcksLocked(proxyKey) {
		g.awaitingAcks.Delete(proxyKey)
	}
}

// awaitsAcksLocked reports whether proxyKey has a window an ACK could still
// end. Callers must hold g.mu.
func (g *publishGate) awaitsAcksLocked(proxyKey string) bool {
	if pf := g.pendingFlips[proxyKey]; pf != nil && len(pf.ahead) > 0 {
		return true
	}
	if state := g.dereferenced[proxyKey]; state != nil {
		for name := range state.since {
			_, acked := state.acked[name]
			_, viaListener := state.viaListener[name]
			if !acked && !viaListener {
				return true
			}
		}
	}
	return false
}

// noteAheadOpenedLocked records that a reference-ahead window for name opens
// with the client's next install. Callers must hold g.mu.
func (g *publishGate) noteAheadOpenedLocked(proxyKey, name string) {
	if g.acks == nil {
		return
	}
	if g.aheadOpened == nil {
		g.aheadOpened = make(map[string]map[string]uint64)
	}
	if g.aheadOpened[proxyKey] == nil {
		g.aheadOpened[proxyKey] = make(map[string]uint64)
	}
	g.aheadOpened[proxyKey][name] = g.installSeq[proxyKey] + 1
}

// aheadAckedLocked reports whether every stream of proxyKey has each of pf's
// new clusters, ready: its last ACKed cluster response, and for an EDS cluster
// its last ACKed endpoint response, came from an install made since the
// cluster's window opened and carries the cluster or its assignment. Tying the
// evidence to the window matters: after A to B and back to A, a stream whose
// last ACK predates A's removal still carries A while the removal may yet
// arrive. A cluster whose readiness no ACK shows keeps its window. Callers
// must hold g.mu.
func (g *publishGate) aheadAckedLocked(proxyKey string, pf *pendingFlipRelease) bool {
	built := pf.wrap.snap.Resources[envoycachetypes.Cluster].Items
	var since uint64
	var assignments []string
	for _, name := range pf.ahead {
		cluster, ok := built[name]
		if !ok || !warmsOnAckedUpdates(cluster) {
			return false
		}
		opened, ok := g.aheadOpened[proxyKey][name]
		if !ok {
			return false
		}
		since = max(since, opened)
		if assignment, eds := endpointResourceNameForCluster(cluster); eds {
			assignments = append(assignments, assignment)
		}
	}
	records := g.installs[proxyKey]
	if !g.acks.AckedResources(proxyKey, envoyresourcev3.ClusterType, pf.ahead) ||
		!ackedSince(records, since, g.acks.AckedVersions(proxyKey, envoyresourcev3.ClusterType), func(r installRecord) string { return r.clusters }) {
		return false
	}
	if len(assignments) == 0 {
		return true
	}
	return g.acks.AckedResources(proxyKey, envoyresourcev3.EndpointType, assignments) &&
		ackedSince(records, since, g.acks.AckedVersions(proxyKey, envoyresourcev3.EndpointType), func(r installRecord) string { return r.endpoints })
}

// warmsOnAckedUpdates reports whether Envoy finishes warming the cluster on
// updates whose ACKs the gate sees: an EDS cluster on its assignment, a STATIC
// one with socket-address endpoints at once. Warming also waits for DNS
// resolution, an initial health check pass, secrets from SDS, and an internal
// listener a pipe or internal address reaches, none of which an ACK shows, so
// such a cluster keeps its window. See
// https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/cluster_manager#cluster-warming
func warmsOnAckedUpdates(resource envoycachetypes.ResourceWithTTL) bool {
	cluster, ok := resource.Resource.(*envoyclusterv3.Cluster)
	if !ok || cluster.GetClusterType() != nil || len(cluster.GetHealthChecks()) > 0 || mayUseSecretDiscovery(cluster) {
		return false
	}
	switch cluster.GetType() {
	case envoyclusterv3.Cluster_EDS:
		return true
	case envoyclusterv3.Cluster_STATIC:
		for _, locality := range cluster.GetLoadAssignment().GetEndpoints() {
			for _, endpoint := range locality.GetLbEndpoints() {
				if endpoint.GetEndpoint().GetAddress().GetSocketAddress() == nil {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

// mayUseSecretDiscovery reports whether the cluster's transport socket may
// fetch secrets from SDS. Anything but plain TLS without SDS, or a raw buffer,
// counts, since another transport socket can wrap one that does.
func mayUseSecretDiscovery(cluster *envoyclusterv3.Cluster) bool {
	if len(cluster.GetTransportSocketMatches()) > 0 {
		return true
	}
	config := cluster.GetTransportSocket().GetTypedConfig()
	if config == nil || config.MessageIs(&envoyrawbufferv3.RawBuffer{}) {
		return false
	}
	tls := &envoytlsv3.UpstreamTlsContext{}
	if !config.MessageIs(tls) || config.UnmarshalTo(tls) != nil {
		return true
	}
	common := tls.GetCommonTlsContext()
	return len(common.GetTlsCertificateSdsSecretConfigs()) > 0 ||
		common.GetValidationContextSdsSecretConfig() != nil ||
		common.GetCombinedValidationContext().GetValidationContextSdsSecretConfig() != nil
}

// dereferencesAckedLocked returns the clusters in state's open windows that
// every stream of proxyKey has stopped routing to: each stream's last ACKed
// route and listener versions were installed no earlier than the window
// opened. Every install since then lacks the cluster's references, since one
// that named it again would have ended the window. A cluster named from a
// listener keeps its window. Callers must hold g.mu.
func (g *publishGate) dereferencesAckedLocked(proxyKey string, state *dereferenceState) []string {
	routes := g.acks.AckedVersions(proxyKey, envoyresourcev3.RouteType)
	listeners := g.acks.AckedVersions(proxyKey, envoyresourcev3.ListenerType)
	if len(listeners) == 0 {
		return nil // no stream has been sent configuration to apply
	}
	records := g.installs[proxyKey]
	var acked []string
	for name := range state.since {
		if _, done := state.acked[name]; done {
			continue
		}
		if _, viaListener := state.viaListener[name]; viaListener {
			continue
		}
		opened, ok := state.opened[name]
		if !ok {
			continue
		}
		if ackedSince(records, opened, routes, func(r installRecord) string { return r.routes }) &&
			ackedSince(records, opened, listeners, func(r installRecord) string { return r.listeners }) {
			acked = append(acked, name)
		}
	}
	slices.Sort(acked)
	return acked
}

// ackedSince reports whether every stream's ACKed version is one installed at
// or after seq. A stream that has ACKed none, or only an older version, has not.
func ackedSince(records []installRecord, seq uint64, streams map[int64]string, version func(installRecord) string) bool {
	for _, acked := range streams {
		if acked == "" {
			return false
		}
		found := false
		for _, r := range records {
			if r.seq >= seq && version(r) == acked {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// recordInstallLocked numbers snap's installation for proxyKey and keeps the
// installs its open windows can still be confirmed by. Callers must hold g.mu.
func (g *publishGate) recordInstallLocked(proxyKey string, snap envoycache.ResourceSnapshot) {
	if g.acks == nil {
		return
	}
	if g.installSeq == nil {
		g.installSeq = make(map[string]uint64)
		g.installs = make(map[string][]installRecord)
	}
	g.installSeq[proxyKey]++
	seq := g.installSeq[proxyKey]
	records := g.installs[proxyKey]
	records = append(records, installRecord{
		seq:       seq,
		clusters:  snap.GetVersion(envoyresourcev3.ClusterType),
		endpoints: snap.GetVersion(envoyresourcev3.EndpointType),
		routes:    snap.GetVersion(envoyresourcev3.RouteType),
		listeners: snap.GetVersion(envoyresourcev3.ListenerType),
	})
	oldest := seq
	if state := g.dereferenced[proxyKey]; state != nil {
		for name := range state.since {
			if opened, ok := state.opened[name]; ok && opened < oldest {
				oldest = opened
			}
		}
	}
	for name, opened := range g.aheadOpened[proxyKey] {
		if _, open := g.aheadSent[proxyKey][name]; !open {
			delete(g.aheadOpened[proxyKey], name)
			continue
		}
		oldest = min(oldest, opened)
	}
	first := 0
	for first < len(records) && records[first].seq < oldest {
		first++
	}
	records = records[first:]
	if len(records) > maxInstallRecords {
		records = records[len(records)-maxInstallRecords:]
	}
	g.installs[proxyKey] = records
}
