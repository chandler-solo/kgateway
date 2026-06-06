package proxy_syncer

import (
	"context"
	"sync"
	"time"

	"istio.io/istio/pkg/kube/controllers"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

const (
	// deferWatchdogInterval is how often the watchdog scans live UCCs for
	// stuck/deferred snapshots. A non-round value is used so the scan does
	// not line up with other periodic loops in the process.
	deferWatchdogInterval = 7 * time.Second

	// deferWatchdogThreshold is how long a live UCC may sit without a current
	// xDS snapshot before the watchdog republishes its last known wrapper. It
	// is intentionally longer than a normal controller restart warmup so the
	// watchdog never races the regular per-client transform during steady
	// startup; it only fires when a client is genuinely stranded.
	deferWatchdogThreshold = 20 * time.Second
)

// deferWatchdogRepublishes counts how many times the watchdog had to
// republish a last-known snapshot for a live UCC that had been deferred
// past the threshold. A persistently nonzero rate indicates the readiness
// guards in snapshotPerClient are stranding clients on stale config.
var deferWatchdogRepublishes = metrics.NewCounter(
	metrics.CounterOpts{
		Subsystem: snapshotSubsystem,
		Name:      "defer_watchdog_republishes_total",
		Help:      "Total number of stuck/deferred per-client snapshots republished by the defer watchdog",
	},
	[]string{gatewayLabel, namespaceLabel},
)

// deferWatchdog is a safety valve that guarantees no live UCC (uniquely
// connected client / Envoy replica) is left without a current xDS snapshot
// indefinitely, independent of KRT event delivery.
//
// snapshotPerClient defers publishing (returns nil) when its per-client
// inputs look incoherent; KRT surfaces that as a Delete, whose handler in
// proxy_syncer.go is an intentional no-op so Envoy keeps its last-published
// snapshot. If a readiness guard becomes permanently true for one UCC, that
// client is stranded on stale endpoints and never republishes. The watchdog
// detects this case and republishes the last wrapper we saw for the client,
// which self-heals the staleness and is harmless when the client is healthy.
type deferWatchdog struct {
	pt   *ProxyTranslator
	uccs krt.Collection[ir.UniqlyConnectedClient]

	mu          sync.Mutex
	lastWrapper map[string]XdsSnapWrapper
	lastChange  map[string]time.Time

	// clock is injectable for tests; defaults to time.Now.
	clock func() time.Time
}

func newDeferWatchdog(pt *ProxyTranslator, uccs krt.Collection[ir.UniqlyConnectedClient]) *deferWatchdog {
	return &deferWatchdog{
		pt:          pt,
		uccs:        uccs,
		lastWrapper: map[string]XdsSnapWrapper{},
		lastChange:  map[string]time.Time{},
		clock:       time.Now,
	}
}

// observe records the latest per-client snapshot event so the watchdog can
// republish the last known good wrapper later if the client gets stranded.
// A Delete is a defer signal: we record the time of the change but keep the
// last wrapper so we still have something to republish.
func (wd *deferWatchdog) observe(e krt.Event[XdsSnapWrapper]) {
	key := e.Latest().ResourceName()

	wd.mu.Lock()
	defer wd.mu.Unlock()

	if e.Event != controllers.EventDelete {
		wd.lastWrapper[key] = e.Latest()
	}
	wd.lastChange[key] = wd.clock()
}

// reconcileOnce republishes the last known wrapper for any live UCC that has
// no current snapshot in the xDS cache and has been in that state for at
// least threshold. Clients with a healthy current snapshot are skipped.
func (wd *deferWatchdog) reconcileOnce(ctx context.Context, threshold time.Duration) {
	liveKeys := make(map[string]struct{})
	for _, ucc := range wd.uccs.List() {
		liveKeys[ucc.ResourceName()] = struct{}{}
	}

	now := wd.clock()

	wd.mu.Lock()
	defer wd.mu.Unlock()

	for key := range liveKeys {
		// A healthy current snapshot in the cache means this client is fine.
		if snap, err := wd.pt.xdsCache.GetSnapshot(key); err == nil && snap != nil {
			continue
		}

		// No snapshot for this live client. Only act once it has been stuck
		// past the threshold, so we never race the normal transform during
		// steady startup.
		lastChange, seen := wd.lastChange[key]
		if seen && now.Sub(lastChange) < threshold {
			continue
		}

		wrapper, ok := wd.lastWrapper[key]
		if !ok {
			// Nothing to republish yet; we have never seen a wrapper for this
			// client. Leave it for the normal transform to populate.
			continue
		}

		cd := getDetailsFromXDSClientResourceName(key)
		var age time.Duration
		if seen {
			age = now.Sub(lastChange)
		}
		logger.Warn(
			"xds snapshot missing/deferred past threshold; republishing last known wrapper",
			"client", key,
			"age", age,
		)
		deferWatchdogRepublishes.Inc(
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		)
		wd.pt.syncXds(ctx, wrapper)
	}
}

// run drives reconcileOnce on a ticker until ctx is cancelled.
func (wd *deferWatchdog) run(ctx context.Context, interval, threshold time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			wd.reconcileOnce(ctx, threshold)
		}
	}
}
