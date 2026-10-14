package proxy_syncer

import (
	"strconv"
	"sync"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

// EDS set versioning.
//
// go-control-plane answers a watch only when the snapshot version differs
// from the version the client last accepted. The EDS version must therefore
// move exactly when the set of assignments a client is served moves: a
// version that moves without the content pushes identical endpoints to every
// client, and a version that stays while the content moves parks the client
// on stale endpoints. Every publication path derives the version from the
// same function of the same inputs, so equal published sets get equal
// versions. No proto is digested for this.
//
//   - A row published directly contributes its row hash (EndpointsHash).
//   - An assignment carried forward from the previously published snapshot
//     contributes the hash it was last published under, so a row that returns
//     with unchanged inputs restores the version it had.
//   - A synthesized empty assignment contributes a hash of its name.
//
// The fold is an XOR, so it is independent of iteration order and, when every
// row is published, equal to the version string this code produced before
// filtering and carry-forward were versioned separately. Two contributions
// cancel only under a 64-bit collision, as before: row hashes and name hashes
// both include the resource name. An input-only change (a policy generation
// bump that leaves the built assignment unchanged) still moves the version;
// removing that push would need a content digest per row, which this
// deliberately avoids.

// endpointSetHashes maps each assignment name in items to its version
// contribution. rowHashes holds the hashes of the rows built for this client,
// keyed by assignment name. carried, when non-nil, answers for assignments
// copied from the previously published snapshot. Anything else is a
// synthesized assignment, versioned by its name.
func endpointSetHashes(items map[string]envoycachetypes.ResourceWithTTL, rowHashes map[string]uint64, carried func(name string) (uint64, bool)) map[string]uint64 {
	out := make(map[string]uint64, len(items))
	for name := range items {
		if h, ok := rowHashes[name]; ok {
			out[name] = h
			continue
		}
		if carried != nil {
			if h, ok := carried(name); ok {
				out[name] = h
				continue
			}
		}
		out[name] = utils.HashString(name)
	}
	return out
}

// endpointSetVersion folds the contributions into the EDS version string.
func endpointSetVersion(hashes map[string]uint64) string {
	var v uint64
	for _, h := range hashes {
		v ^= h
	}
	return strconv.FormatUint(v, 10)
}

// versionEndpointSet returns res versioned by hashes.
func versionEndpointSet(res envoycache.Resources, hashes map[string]uint64) envoycache.Resources {
	return envoycache.Resources{Version: endpointSetVersion(hashes), Items: res.Items}
}

// publishedEndpointHashes remembers, per client, the contribution each
// assignment was published under in the snapshot currently in the cache, so
// a carried-forward assignment keeps that contribution. Safe for concurrent
// use; a nil receiver remembers nothing.
type publishedEndpointHashes struct {
	mu       sync.Mutex
	byClient map[string]map[string]uint64
}

func newPublishedEndpointHashes() *publishedEndpointHashes {
	return &publishedEndpointHashes{byClient: make(map[string]map[string]uint64)}
}

// record replaces the remembered contributions for proxyKey.
func (p *publishedEndpointHashes) record(proxyKey string, hashes map[string]uint64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byClient[proxyKey] = hashes
}

// lookup returns a function answering for the contributions remembered for
// proxyKey at the time of the call.
func (p *publishedEndpointHashes) lookup(proxyKey string) func(name string) (uint64, bool) {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	hashes := p.byClient[proxyKey]
	p.mu.Unlock()
	if hashes == nil {
		return nil
	}
	return func(name string) (uint64, bool) {
		h, ok := hashes[name]
		return h, ok
	}
}
