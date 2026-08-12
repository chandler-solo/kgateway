package statussync

import "sync"

// ClearDeferrals tracks, per resource, whether a pure-retraction write (an empty desired
// status that would remove every entry this controller owns) has already been deferred one
// queue pass. See Writer.ClearsOwnedEntries for the invariant this protects.
//
// The state machine per resource is: first retraction decision → mark and defer; follow-up
// retraction decision → consume the mark and proceed; any non-retraction decision (a real
// write, a converged no-op, or the resource disappearing) → drop the mark. Marks therefore
// live only for the one pass between a deferral and its follow-up, so the map stays bounded
// by the number of resources currently in that window.
type ClearDeferrals struct {
	mu      sync.Mutex
	pending map[Resource]struct{}
}

func NewClearDeferrals() *ClearDeferrals {
	return &ClearDeferrals{pending: map[Resource]struct{}{}}
}

// deferOnce reports whether this retraction should be deferred: true the first time a
// resource reaches a retraction decision, false on the follow-up pass, where the mark is
// consumed so a later, separate retraction episode defers again.
func (c *ClearDeferrals) deferOnce(res Resource) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.pending[res]; exists {
		delete(c.pending, res)
		return false
	}
	c.pending[res] = struct{}{}
	return true
}

// reset drops any pending deferral mark for res. Called whenever a decision for res is not
// a retraction, so stale marks cannot make some future retraction skip its deferral.
func (c *ClearDeferrals) reset(res Resource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, res)
}
