package proxy_syncer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"time"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
	"istio.io/istio/pkg/kube/controllers"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/query"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	kmetrics "github.com/kgateway-dev/kgateway/v2/pkg/krtcollections/metrics"
	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
	plug "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/collections"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	"github.com/kgateway-dev/kgateway/v2/pkg/reports"
	"github.com/kgateway-dev/kgateway/v2/pkg/validator"
)

var _ manager.LeaderElectionRunnable = &ProxySyncer{}

// ProxySyncer orchestrates the translation of K8s Gateway CRs to xDS
// and setting the output xDS snapshot in the envoy snapshot cache,
// resulting in each connected proxy getting the correct configuration.
// It runs on all pods (leader or follower) as the xDS snapshot must be consistent across pods.
// It queues the status reports resulting from translation on the `reportQueue` && `backendPolicyReportQueue`
// to be handled by the statusSyncer.
type ProxySyncer struct {
	controllerName string

	mgr        manager.Manager
	commonCols *collections.CommonCollections
	translator *translator.CombinedTranslator
	plugins    plug.Plugin

	apiClient       apiclient.Client
	proxyTranslator ProxyTranslator

	uniqueClients krt.Collection[ir.UniqlyConnectedClient]

	statusReport            krt.Singleton[report]
	backendPolicyReport     krt.Singleton[report]
	mostXdsSnapshots        krt.Collection[GatewayXdsResources]
	perclientSnapCollection krt.Collection[XdsSnapWrapper]

	// perClientHeartbeat is fired on a timer to periodically re-run the per-client
	// cluster/endpoint collections, bounding how long a lost recompute edge can
	// strand a client on stale/empty config (#14184).
	perClientHeartbeat *krt.RecomputeTrigger

	// reconciler reclaims xDS cache entries for departed clients and tracks
	// recovery-from-deferral; both run on the heartbeat loop (#14184).
	reconciler *perClientReconciler

	waitForSync []cache.InformerSynced
	ready       atomic.Bool

	reportQueue              utils.AsyncQueue[reports.ReportMap]
	backendPolicyReportQueue utils.AsyncQueue[reports.ReportMap]
}

type GatewayXdsResources struct {
	types.NamespacedName

	reports reports.ReportMap
	// Clusters are items in the CDS response payload.
	// +krtEqualsTodo include CDC resources in equality for diff detection
	Clusters     []envoycachetypes.ResourceWithTTL
	ClustersHash uint64

	// Routes are items in the RDS response payload.
	Routes envoycache.Resources

	// Listeners are items in the LDS response payload.
	Listeners envoycache.Resources

	// Secrets are items in the SDS response payload.
	Secrets envoycache.Resources

	// ReferencedClusters is the set of cluster names referenced by Routes and
	// Listeners. It is derived from the proto contents, so it is a pure function
	// of Routes.Version and Listeners.Version (already covered by Equals).
	// Carried through XdsSnapWrapper to publication so the missing-cluster
	// check never re-walks LDS/RDS protos per client per event.
	// +noKrtEquals
	ReferencedClusters map[string]struct{}
}

func (r GatewayXdsResources) ResourceName() string {
	return xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, r.Namespace, r.Name)
}

func (r GatewayXdsResources) Equals(in GatewayXdsResources) bool {
	return r.NamespacedName == in.NamespacedName &&
		report{r.reports}.Equals(report{in.reports}) &&
		r.ClustersHash == in.ClustersHash &&
		r.Routes.Version == in.Routes.Version &&
		r.Listeners.Version == in.Listeners.Version &&
		r.Secrets.Version == in.Secrets.Version
}

func sliceToResourcesHash[T proto.Message](slice []T) ([]envoycachetypes.ResourceWithTTL, uint64) {
	var slicePb []envoycachetypes.ResourceWithTTL
	var resourcesHash uint64
	for _, r := range slice {
		var m proto.Message = r
		hash := utils.HashProto(r)
		slicePb = append(slicePb, envoycachetypes.ResourceWithTTL{Resource: m})
		resourcesHash ^= hash
	}

	return slicePb, resourcesHash
}

func sliceToResources[T proto.Message](slice []T) envoycache.Resources {
	r, h := sliceToResourcesHash(slice)
	return envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", h), r)
}

func toResources(gw ir.Gateway, xdsSnap irtranslator.TranslationResult, r reports.ReportMap) *GatewayXdsResources {
	c, ch := sliceToResourcesHash(xdsSnap.ExtraClusters)
	routes := sliceToResources(xdsSnap.Routes)
	listeners := sliceToResources(xdsSnap.Listeners)
	return &GatewayXdsResources{
		NamespacedName: types.NamespacedName{
			Namespace: gw.Obj.GetNamespace(),
			Name:      gw.Obj.GetName(),
		},
		reports:            r,
		ClustersHash:       ch,
		Clusters:           c,
		Routes:             routes,
		Listeners:          listeners,
		Secrets:            sliceToResources(xdsSnap.Secrets),
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}
}

// NewProxySyncer returns a ProxySyncer runnable
// The provided GatewayInputChannels are used to trigger syncs.
func NewProxySyncer(
	ctx context.Context,
	controllerName string,
	mgr manager.Manager,
	client apiclient.Client,
	uniqueClients krt.Collection[ir.UniqlyConnectedClient],
	mergedPlugins plug.Plugin,
	commonCols *collections.CommonCollections,
	xdsCache envoycache.SnapshotCache,
	validator validator.Validator,
) *ProxySyncer {
	return &ProxySyncer{
		controllerName:           controllerName,
		commonCols:               commonCols,
		mgr:                      mgr,
		apiClient:                client,
		proxyTranslator:          NewProxyTranslator(xdsCache),
		uniqueClients:            uniqueClients,
		translator:               translator.NewCombinedTranslator(ctx, mergedPlugins, commonCols, validator),
		plugins:                  mergedPlugins,
		reportQueue:              utils.NewAsyncQueue[reports.ReportMap](),
		backendPolicyReportQueue: utils.NewAsyncQueue[reports.ReportMap](),
	}
}

type ProxyTranslator struct {
	xdsCache envoycache.SnapshotCache
	// gate is the publication policy and bookkeeping seam (warm-up deferral,
	// atomic publish commit, stuck tracking; see syncXds). Nil disables it
	// (used by tests); publication then goes straight to the cache.
	gate publicationGate
}

func NewProxyTranslator(xdsCache envoycache.SnapshotCache) ProxyTranslator {
	return ProxyTranslator{
		xdsCache: xdsCache,
	}
}

type report struct {
	// lower case so krt doesn't error in debug handler
	reportMap reports.ReportMap
}

func (r report) ResourceName() string {
	return "report"
}

// do we really need this for a singleton?
func (r report) Equals(in report) bool {
	if !maps.Equal(r.reportMap.Gateways, in.reportMap.Gateways) {
		return false
	}
	if !maps.EqualFunc(r.reportMap.ListenerSets, in.reportMap.ListenerSets,
		func(a, b map[types.NamespacedName]*reports.ListenerSetReport) bool {
			return maps.Equal(a, b)
		}) {
		return false
	}
	if !maps.Equal(r.reportMap.HTTPRoutes, in.reportMap.HTTPRoutes) {
		return false
	}
	if !maps.Equal(r.reportMap.TCPRoutes, in.reportMap.TCPRoutes) {
		return false
	}
	if !maps.Equal(r.reportMap.TLSRoutes, in.reportMap.TLSRoutes) {
		return false
	}
	if !maps.Equal(r.reportMap.Policies, in.reportMap.Policies) {
		return false
	}
	return true
}

var logger = logging.New("proxy_syncer")

func (s *ProxySyncer) Init(ctx context.Context, krtopts krtutil.KrtOptions) {
	queries := query.NewData(s.commonCols)

	gatewayBackendVariants := newGatewayBackendVariants(
		ctx,
		krtopts,
		queries,
		s.commonCols.GatewayIndex.Gateways,
	)
	gatewayBackendVariantBackends := krt.NewCollection(gatewayBackendVariants, func(kctx krt.HandlerContext, backendForGateway gatewayScopedBackend) *ir.BackendObjectIR {
		if backendForGateway.backend == nil {
			return nil
		}
		backend := *backendForGateway.backend
		return &backend
	}, krtopts.ToOptions("GatewayBackendClientCertificateVariantBackends")...)
	gatewayBackendVariantBackendsWithPolicy, _ := s.commonCols.BackendIndex.AttachPoliciesToCollection(
		gatewayBackendVariantBackends,
		"GatewayBackendClientCertificateVariantBackendsWithPolicy",
	)
	gatewayBackendVariantEndpoints := newGatewayBackendVariantEndpoints(krtopts, gatewayBackendVariants, s.commonCols.Endpoints)

	// all backends with policies attached in a single collection
	finalBackends := krt.JoinCollection(
		append(s.commonCols.BackendIndex.BackendsWithPolicy(), gatewayBackendVariantBackendsWithPolicy),
		// WithJoinUnchecked enables a more optimized lookup on the hotpath by assuming we do not have any overlapping ResourceName
		// in the backend collection.
		append(krtopts.ToOptions("FinalBackends"), krt.WithJoinUnchecked())...)
	finalBackendsWithPolicyStatus := krt.JoinCollection(s.commonCols.BackendIndex.BackendsWithPolicyRequiringStatus(),
		// WithJoinUnchecked enables a more optimized lookup on the hotpath by assuming we do not have any overlapping ResourceName
		// in the backend collection.
		append(krtopts.ToOptions("FinalBackendsWithPolicyStatus"), krt.WithJoinUnchecked())...)
	allEndpoints := krt.JoinCollection(
		[]krt.Collection[ir.EndpointsForBackend]{s.commonCols.Endpoints, gatewayBackendVariantEndpoints},
		krtopts.ToOptions("AllEndpoints")...,
	)

	s.translator.Init(ctx)

	s.mostXdsSnapshots = krt.NewCollection(s.commonCols.GatewayIndex.Gateways, func(kctx krt.HandlerContext, gw ir.Gateway) *GatewayXdsResources {
		// Note: s.commonCols.GatewayIndex.Gateways is already filtered to only include Gateways
		// with controllerName matching s.controllerName (envoy controller). The filtering happens
		// in GatewaysForEnvoyTransformationFunc in pkg/krtcollections/policy.go
		logger.Debug("building proxy for kube gw", "name", client.ObjectKeyFromObject(gw.Obj), "version", gw.Obj.GetResourceVersion())

		xdsSnap, rm := s.translator.TranslateGateway(kctx, ctx, gw)
		if xdsSnap == nil {
			return nil
		}

		return toResources(gw, *xdsSnap, rm)
	}, krtopts.ToOptions("MostXdsSnapshots")...)

	// Heartbeat that periodically forces the per-client collections to recompute,
	// so a deferred snapshot can never become permanent (#14184). startSynced=true
	// so dependents are not blocked waiting on it.
	s.perClientHeartbeat = krt.NewRecomputeTrigger(true, krtopts.ToOptions("PerClientHeartbeat")...)
	s.reconciler = newPerClientReconciler(s.proxyTranslator.xdsCache, s.uniqueClients, perClientReclaimGrace)
	s.reconciler.roleHasSnapshot = func(role string) bool {
		return s.mostXdsSnapshots.GetKey(role) != nil
	}
	s.proxyTranslator.gate = s.reconciler

	epPerClient := NewPerClientEnvoyEndpoints(
		krtopts,
		s.uniqueClients,
		newFinalBackendEndpoints(krtopts, finalBackends, allEndpoints),
		s.translator.TranslateEndpoints,
		s.perClientHeartbeat,
	)
	clustersPerClient := NewPerClientEnvoyClusters(
		ctx,
		krtopts,
		s.translator.GetBackendTranslator(),
		finalBackends,
		s.uniqueClients,
		s.perClientHeartbeat,
	)

	s.perclientSnapCollection = snapshotPerClient(
		krtopts,
		s.uniqueClients,
		s.mostXdsSnapshots,
		epPerClient,
		clustersPerClient,
		s.perClientHeartbeat,
	)

	excludedPolicyKinds := make(map[schema.GroupKind]struct{})
	for gk, plugin := range s.plugins.ContributesPolicies {
		if plugin.PolicyStatusFromGatewayReports {
			excludedPolicyKinds[gk] = struct{}{}
		}
	}

	s.backendPolicyReport = krt.NewSingleton(func(kctx krt.HandlerContext) *report {
		backends := krt.Fetch(kctx, finalBackendsWithPolicyStatus)
		merged := GenerateBackendPolicyReport(backends, excludedPolicyKinds)

		for _, plugin := range s.plugins.ContributesPolicies {
			if plugin.ProcessPolicyStaleStatusMarkers != nil && plugin.ProcessBackend != nil && !plugin.PolicyStatusFromGatewayReports {
				plugin.ProcessPolicyStaleStatusMarkers(kctx, &merged)
			}
		}

		return &report{merged}
	}, krtopts.ToOptions("BackendsPolicyReport")...)

	// as proxies are created, they also contain a reportMap containing status for the Gateway and associated xRoutes (really parentRefs)
	// here we will merge reports that are per-Proxy to a singleton Report used to persist to k8s on a timer
	s.statusReport = krt.NewSingleton(func(kctx krt.HandlerContext) *report {
		proxies := krt.Fetch(kctx, s.mostXdsSnapshots)

		merged := mergeProxyReports(proxies)

		// Process status markers
		objStatus := krt.Fetch(kctx, s.commonCols.Routes.GetHTTPRouteStatusMarkers())
		s.commonCols.Routes.ProcessHTTPRouteStatusMarkers(objStatus, merged)

		for _, plugin := range s.plugins.ContributesPolicies {
			if plugin.ProcessPolicyStaleStatusMarkers != nil && (plugin.ProcessBackend == nil || plugin.PolicyStatusFromGatewayReports) {
				plugin.ProcessPolicyStaleStatusMarkers(kctx, &merged)
			}
		}

		return &report{merged}
	})

	s.waitForSync = []cache.InformerSynced{
		s.commonCols.HasSynced,
		finalBackends.HasSynced,
		s.perclientSnapCollection.HasSynced,
		s.mostXdsSnapshots.HasSynced,
		s.plugins.HasSynced,
		s.translator.HasSynced,
	}
}

func mergeProxyReports(
	proxies []GatewayXdsResources,
) reports.ReportMap {
	merged := reports.NewReportMap()
	for _, p := range proxies {
		// 1. merge GW Reports for all Proxies' status reports
		maps.Copy(merged.Gateways, p.reports.Gateways)

		// 2. merge LS Reports for all Proxies' status reports
		maps.Copy(merged.ListenerSets, p.reports.ListenerSets)

		// 3. merge httproute parentRefs into RouteReports
		for rnn, rr := range p.reports.HTTPRoutes {
			// if we haven't encountered this route, just copy it over completely
			old := merged.HTTPRoutes[rnn]
			if old == nil {
				merged.HTTPRoutes[rnn] = rr
				continue
			}
			// else, this route has already been seen for a proxy, merge this proxy's parents
			// into the merged report
			maps.Copy(merged.HTTPRoutes[rnn].Parents, rr.Parents)
		}

		// 4. merge tcproute parentRefs into RouteReports
		for rnn, rr := range p.reports.TCPRoutes {
			// if we haven't encountered this route, just copy it over completely
			old := merged.TCPRoutes[rnn]
			if old == nil {
				merged.TCPRoutes[rnn] = rr
				continue
			}
			// else, this route has already been seen for a proxy, merge this proxy's parents
			// into the merged report
			maps.Copy(merged.TCPRoutes[rnn].Parents, rr.Parents)
		}

		for rnn, rr := range p.reports.TLSRoutes {
			// if we haven't encountered this route, just copy it over completely
			old := merged.TLSRoutes[rnn]
			if old == nil {
				merged.TLSRoutes[rnn] = rr
				continue
			}
			// else, this route has already been seen for a proxy, merge this proxy's parents
			// into the merged report
			maps.Copy(merged.TLSRoutes[rnn].Parents, rr.Parents)
		}

		for rnn, rr := range p.reports.GRPCRoutes {
			// if we haven't encountered this route, just copy it over completely
			old := merged.GRPCRoutes[rnn]
			if old == nil {
				merged.GRPCRoutes[rnn] = rr
				continue
			}
			// else, this route has already been seen for a proxy, merge this proxy's parents
			// into the merged report
			maps.Copy(merged.GRPCRoutes[rnn].Parents, rr.Parents)
		}

		for key, report := range p.reports.Policies {
			// if we haven't encountered this policy, just copy it over completely
			old := merged.Policies[key]
			if old == nil {
				merged.Policies[key] = report
				continue
			}
			// else, let's merge our parentRefs into the existing map
			// obsGen will stay as-is...
			maps.Copy(merged.Policies[key].Ancestors, report.Ancestors)
		}
	}

	return merged
}

func (s *ProxySyncer) Start(ctx context.Context) error {
	logger.Info("starting Proxy Syncer", "controller", s.controllerName)

	// wait for krt collections to sync
	logger.Info("waiting for cache to sync")
	s.apiClient.WaitForCacheSync(
		"kube gw proxy syncer",
		ctx.Done(),
		s.waitForSync...,
	)

	// wait for ctrl-rtime caches to sync before accepting events
	if !s.mgr.GetCache().WaitForCacheSync(ctx) {
		return errors.New("kube gateway proxy syncer sync loop waiting for all caches to sync failed")
	}
	logger.Info("caches warm!")

	// caches are warm, now we can do registrations

	// latestReport will be constantly updated to contain the merged status report for Kube Gateway status
	// when timer ticks, we will use the state of the mergedReports at that point in time to sync the status to k8s
	s.statusReport.Register(func(o krt.Event[report]) {
		if o.Event == controllers.EventDelete {
			// TODO: handle garbage collection
			return
		}
		s.reportQueue.Enqueue(o.Latest().reportMap)
	})

	s.backendPolicyReport.Register(func(o krt.Event[report]) {
		if o.Event == controllers.EventDelete {
			return
		}
		s.backendPolicyReportQueue.Enqueue(o.Latest().reportMap)
	})

	s.perclientSnapCollection.RegisterBatch(func(o []krt.Event[XdsSnapWrapper]) {
		for _, e := range o {
			if e.Event != controllers.EventDelete {
				// Snapshots reaching here are consistent by construction (the
				// wait-for-consistency gates in snapshotPerClient withheld
				// anything incomplete); syncXds commits the publish atomically
				// with its per-client bookkeeping via the reconciler.
				s.proxyTranslator.syncXds(ctx, e.Latest())
				cd := getDetailsFromXDSClientResourceName(e.Latest().ResourceName())
				kmetrics.EndResourceXDSSync(kmetrics.ResourceSyncDetails{
					Namespace:    cd.Namespace,
					Gateway:      cd.Gateway,
					ResourceName: cd.Gateway,
				})
			} else {
				// Retain the last-good snapshot during a defer (do NOT ClearSnapshot
				// here): snapshotPerClient returns nil while its consistency gates
				// hold, and clearing would withdraw Envoy's last coherent config and
				// cause 500/NC on valid routes. The heartbeat re-runs the per-client
				// path so the gates reliably open once inputs are coherent again.
				//
				// This branch also fires when a UCC truly departs (Envoy replaced,
				// scaled down). The reconciler classifies the two by liveness: a
				// still-connected client is marked stuck (so the heartbeat heals
				// it); a departed one just starts the reclaim clock, so routine pod
				// churn does not keep the heartbeat firing — and the retained cache
				// entry is cleared only after a grace period, which fixes the
				// previously-unbounded SnapshotCache leak (#14184).
				if s.reconciler != nil {
					s.reconciler.observeSnapshotDelete(e.Latest().ResourceName())
				}
			}
		}
	}, true)

	// Two independent maintenance loops (#14184): the heartbeat re-runs the
	// per-client collections when a client is stuck without a current snapshot,
	// and the reclaimer clears cache entries for departed clients. They are
	// deliberately separate so disabling the (expensive) heartbeat does not
	// silently re-introduce the (cheaply fixed) cache leak.
	go s.runPerClientHeartbeat(ctx)
	go s.runPerClientReclaim(ctx)

	s.ready.Store(true)
	<-ctx.Done()
	return nil
}

// perClientHeartbeatDefaultInterval is how often the heartbeat checks for stuck
// clients, and therefore the worst-case heal latency once a client is stuck. The
// expensive recompute itself is demand-driven (see runPerClientHeartbeat), so this
// governs detection cadence, not steady-state cost. Override with
// KGW_PERCLIENT_HEARTBEAT_INTERVAL (a Go duration); a value <= 0 disables the
// heartbeat only — cache reclaim runs on its own loop and is unaffected.
const perClientHeartbeatDefaultInterval = 30 * time.Second

// perClientHeartbeatIntervalEnv is the env var that overrides the heartbeat interval.
const perClientHeartbeatIntervalEnv = "KGW_PERCLIENT_HEARTBEAT_INTERVAL"

// perClientHeartbeatFallbackEvery is the number of heartbeat ticks between
// unconditional recomputes. A hole in the per-client collections only causes
// harm once snapshotPerClient defers on it, which marks the client stuck and
// makes the very next tick fire, so the unconditional pass only covers signals
// the stuck tracking cannot see — it is a safety margin, not the primary heal
// path, and can be rare.
const perClientHeartbeatFallbackEvery = 10

// perClientReclaimInterval is how often departed clients' retained xDS cache
// entries are reclaimed. The scan is a list plus a map walk — cheap enough to run
// unconditionally. Offset from the heartbeat interval to avoid lockstep.
const perClientReclaimInterval = 31 * time.Second

// perClientReclaimGrace is how long a client must be absent from the connected set
// before its retained xDS cache entry is reclaimed. Comfortably longer than a
// reconnect blip so a briefly-disconnected client is never cleared.
const perClientReclaimGrace = 2 * time.Minute

func perClientHeartbeatInterval() time.Duration {
	v := os.Getenv(perClientHeartbeatIntervalEnv)
	if v == "" {
		return perClientHeartbeatDefaultInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		logger.Warn("invalid "+perClientHeartbeatIntervalEnv+", using default",
			"value", v, "default", perClientHeartbeatDefaultInterval, "error", err)
		return perClientHeartbeatDefaultInterval
	}
	return d
}

// runJitteredLoop runs body on every tick of a jittered ticker until ctx is
// done. The first run happens after a random fraction of interval plus one full
// interval, so HA replicas do not run in lockstep; the jitter select honors
// ctx.Done so shutdown is never delayed.
func runJitteredLoop(ctx context.Context, interval time.Duration, body func()) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(rand.N(interval)): //nolint:gosec // G404: jitter only, not security-sensitive
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			body()
		}
	}
}

// runPerClientHeartbeat is the level-triggered reconcile loop for the per-client
// collections (#14184). Firing the trigger re-runs every transform on the
// per-client path against current inputs, which heals any rows a missed or
// stale recompute left absent — and therefore bounds how long the
// wait-for-consistency gates in snapshotPerClient can stay closed. At
// production scale that is tens of thousands of re-translations, so it is
// demand-driven: each tick fires only if some connected client is stuck
// (deferred, or connected-but-never-published with a publishable role), plus a
// rare unconditional pass as a safety margin. A healthy fleet therefore pays
// only a cheap per-tick check; the worst case (a client stuck across
// consecutive ticks) costs no more than the old unconditional design.
// Unchanged recomputes are hash-suppressed by KRT, so even a firing tick
// causes no snapshot churn.
func (s *ProxySyncer) runPerClientHeartbeat(ctx context.Context) {
	if s.perClientHeartbeat == nil {
		return
	}
	interval := perClientHeartbeatInterval()
	if interval <= 0 {
		logger.Info("per-client heartbeat disabled", "interval", interval)
		return
	}
	var ticks int
	runJitteredLoop(ctx, interval, func() {
		ticks++
		if s.reconciler != nil && !s.reconciler.hasStuckClients() &&
			ticks%perClientHeartbeatFallbackEvery != 0 {
			return
		}
		s.perClientHeartbeat.TriggerRecomputation()
	})
}

// runPerClientReclaim clears retained xDS cache entries for clients that left the
// connected set longer than the grace period ago. Deliberately independent of the
// heartbeat loop: reclaiming is cheap and fixes an unbounded leak, so it must not
// be disabled as a side effect of turning off the (expensive) heartbeat.
func (s *ProxySyncer) runPerClientReclaim(ctx context.Context) {
	if s.reconciler == nil {
		return
	}
	runJitteredLoop(ctx, perClientReclaimInterval, func() {
		for _, key := range s.reconciler.reconcile() {
			recordSnapshotReclaimed(key)
		}
	})
}

func (s *ProxySyncer) HasSynced() bool {
	return s.ready.Load()
}

// NeedLeaderElection returns false to ensure that the proxySyncer runs on all pods (leader and followers)
func (r *ProxySyncer) NeedLeaderElection() bool {
	return false
}

// ReportQueue returns the queue that contains the latest status reports.
// It will be constantly updated to contain the merged status report for Kube Gateway status.
func (s *ProxySyncer) ReportQueue() utils.AsyncQueue[reports.ReportMap] {
	return s.reportQueue
}

// BackendPolicyReportQueue returns the queue that contains the latest status reports for all backend policies.
// It will be constantly updated to contain the merged status report for backend policies.
func (s *ProxySyncer) BackendPolicyReportQueue() utils.AsyncQueue[reports.ReportMap] {
	return s.backendPolicyReportQueue
}

// WaitForSync returns a list of functions that can be used to determine if all its informers have synced.
// This is useful for determining if caches have synced.
// It must be called only after `Init()`.
func (s *ProxySyncer) CacheSyncs() []cache.InformerSynced {
	return s.waitForSync
}

type resourcesStringer envoycache.Resources

func (r resourcesStringer) String() string {
	return fmt.Sprintf("len: %d, version %s", len(r.Items), r.Version)
}
