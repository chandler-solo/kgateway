package proxy_syncer

import (
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
)

// xdsScoping is the xDS scoping configuration, resolved once
// from Settings. Scoped guards every scoping path (emitted-set walk,
// CDS/EDS and backend-owned LDS/SDS filtering, transition windows); when false
// none run and the syncer's snapshot publishes unchanged.
type xdsScoping struct {
	mode apisettings.XdsScopingMode
	// dereferenceGrace is the de-reference grace. Read it through
	// DereferenceGrace, which is 0 when scoping is off.
	dereferenceGrace time.Duration
	// referenceAhead is how long a route update onto a newly-emitted cluster is
	// held. Read only through ReferenceAhead, which ignores it with scoping
	// off.
	referenceAhead time.Duration
	// claims are plugins' declared destinations the configuration never names,
	// collected once at startup.
	claims emissionClaims
}

func xdsScopingFrom(s apisettings.Settings, policies sdk.ContributesPolicies) xdsScoping {
	if s.XdsScopingMode != apisettings.XdsScopingReferenced {
		// Unscoped: no transition windows, and no plugin's claim code runs.
		return xdsScoping{mode: s.XdsScopingMode}
	}
	return xdsScoping{
		mode:             s.XdsScopingMode,
		dereferenceGrace: s.XdsScopingDereferenceGrace,
		referenceAhead:   s.XdsScopingReferenceAhead,
		claims:           collectEmissionClaims(policies),
	}
}

// Claims are the plugins' declared destinations, used both to decide whether a
// gateway can be scoped and to keep the claimed clusters.
func (c xdsScoping) Claims() emissionClaims { return c.claims }

// ReferenceAhead is the reference-ahead window, or 0 when xDS scoping is off:
// with every backend emitted, no cluster is new to a client.
func (c xdsScoping) ReferenceAhead() time.Duration {
	if !c.Scoped() {
		return 0
	}
	return c.referenceAhead
}

// DereferenceGrace is how long a de-referenced cluster stays published; 0 when
// xDS scoping is off, where no cluster is ever de-referenced.
func (c xdsScoping) DereferenceGrace() time.Duration {
	if !c.Scoped() {
		return 0
	}
	return c.dereferenceGrace
}

// Scoped reports whether CDS, EDS and backend-owned LDS/SDS resources
// are scoped to referenced clusters. False, the default, emits every backend
// in discovery scope with its endpoints, listeners and secrets.
func (c xdsScoping) Scoped() bool {
	return c.mode == apisettings.XdsScopingReferenced
}

// emittedClustersFor returns the zero value when xDS scoping is off. Nothing
// consults it then, and skipping the full proto walk keeps it off the
// translation path. Otherwise claims with DestinationsFor decide each cluster
// specifier use here, once per gateway rebuild.
func emittedClustersFor(scoping xdsScoping, routes, listeners envoycache.Resources) emittedClusters {
	if !scoping.Scoped() {
		return emittedClusters{}
	}
	emitted := collectReferencedClustersForEmission(routes, listeners)
	emitted.deriveClaims(scoping.Claims())
	return emitted
}

// scopedXds returns the enabled configuration for tests; the zero value is
// disabled.
func scopedXds() xdsScoping {
	return xdsScoping{mode: apisettings.XdsScopingReferenced}
}

// scopedXdsWithGrace is scopedXds with a de-reference grace.
func scopedXdsWithGrace(grace time.Duration) xdsScoping {
	s := scopedXds()
	s.dereferenceGrace = grace
	return s
}

// scopedXdsWithReferenceAhead is scopedXds with a reference-ahead
// window, for tests.
func scopedXdsWithReferenceAhead(ahead time.Duration) xdsScoping {
	s := scopedXds()
	s.referenceAhead = ahead
	return s
}
