package statussync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

const (
	deferTestController  = "us.example/controller"
	deferOtherController = "other.example/controller"
)

func deferTestResource() Resource {
	return Resource{
		GroupVersionKind: wellknown.HTTPRouteGVK,
		NamespacedName:   types.NamespacedName{Namespace: "ns", Name: "route"},
	}
}

func ourParent() gwv1.RouteParentStatus {
	return gwv1.RouteParentStatus{
		ParentRef:      gwv1.ParentReference{Name: "gw"},
		ControllerName: deferTestController,
		Conditions: []metav1.Condition{{
			Type:               string(gwv1.RouteConditionAccepted),
			Status:             metav1.ConditionTrue,
			Reason:             string(gwv1.RouteReasonAccepted),
			LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second)),
		}},
	}
}

func foreignParent() gwv1.RouteParentStatus {
	return gwv1.RouteParentStatus{
		ParentRef:      gwv1.ParentReference{Name: "their-gw"},
		ControllerName: deferOtherController,
	}
}

// deferHarness drives one route writer against an in-memory object, so every pass is a
// deliberate ApplyStatus call with no informers or queues involved.
type deferHarness struct {
	live        *gwv1.HTTPRoute
	desired     func() gwv1.RouteStatus
	writes      int
	requeues    int
	onSyncCalls int
	writer      Writer[*gwv1.HTTPRoute, gwv1.RouteStatus]
}

func newDeferHarness() *deferHarness {
	h := &deferHarness{
		live: &gwv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "ns", ResourceVersion: "1"},
			Status: gwv1.HTTPRouteStatus{RouteStatus: gwv1.RouteStatus{
				Parents: []gwv1.RouteParentStatus{foreignParent(), ourParent()},
			}},
		},
		// The ambiguous leader-startup state: the reduction is empty, so desired retracts.
		desired: func() gwv1.RouteStatus { return gwv1.RouteStatus{} },
	}
	h.writer = Writer[*gwv1.HTTPRoute, gwv1.RouteStatus]{
		Name:    "route",
		Current: func(Resource) *gwv1.HTTPRoute { return h.live },
		Desired: func(*gwv1.HTTPRoute) (gwv1.RouteStatus, bool) { return h.desired(), true },
		UpdateStatus: func(om metav1.ObjectMeta, s gwv1.RouteStatus) error {
			h.writes++
			next := h.live.DeepCopy()
			next.Status.RouteStatus = s
			h.live = next
			return nil
		},
		GetStatus: func(o *gwv1.HTTPRoute) gwv1.RouteStatus { return o.Status.RouteStatus },
		Merge: func(current *gwv1.HTTPRoute, desired gwv1.RouteStatus) gwv1.RouteStatus {
			desired.Parents = MergeRouteParentStatuses(deferTestController, current.Status.Parents, desired.Parents)
			return desired
		},
		ClearsOwnedEntries: func(current *gwv1.HTTPRoute, merged gwv1.RouteStatus) bool {
			return OwnsAnyRouteParent(deferTestController, current.Status.Parents) &&
				!OwnsAnyRouteParent(deferTestController, merged.Parents)
		},
		Requeue:        func(Resource) { h.requeues++ },
		ClearDeferrals: NewClearDeferrals(),
		OnSync: func(Resource, *gwv1.HTTPRoute, gwv1.RouteStatus, time.Duration, error) {
			h.onSyncCalls++
		},
	}
	return h
}

// TestWriterDefersRetractionThenClears walks the full genuine-detach episode: the first
// retraction decision defers (requeue, no write, sync left open), the follow-up pass
// executes the clear preserving foreign entries, and a further pass is a no-op.
func TestWriterDefersRetractionThenClears(t *testing.T) {
	h := newDeferHarness()
	ctx := context.Background()
	res := deferTestResource()

	h.writer.ApplyStatus(ctx, res)
	require.Zero(t, h.writes, "the first retraction decision must defer, not write")
	require.Equal(t, 1, h.requeues, "the deferral must schedule exactly one follow-up pass")
	require.Zero(t, h.onSyncCalls, "a deferred pass must leave the resource's sync open")

	h.writer.ApplyStatus(ctx, res)
	require.Equal(t, 1, h.writes, "the follow-up pass must execute the retraction")
	require.Equal(t, 1, h.requeues, "an executed retraction must not requeue again")
	require.Equal(t, 1, h.onSyncCalls)
	require.False(t, OwnsAnyRouteParent(deferTestController, h.live.Status.Parents))
	require.True(t, OwnsAnyRouteParent(deferOtherController, h.live.Status.Parents),
		"the foreign controller's parent must be preserved by the clear")

	h.writer.ApplyStatus(ctx, res)
	require.Equal(t, 1, h.writes, "the cleared status is the fixed point")
}

// TestWriterDeferredRetractionYieldsToConvergence pins the point of the deferral: when the
// pipeline converges between the first look and the follow-up pass, the veteran status is
// refreshed and no retraction ever executes — and the consumed mark does not leak into a
// later, genuine retraction episode, which defers again from scratch.
func TestWriterDeferredRetractionYieldsToConvergence(t *testing.T) {
	h := newDeferHarness()
	ctx := context.Background()
	res := deferTestResource()

	h.writer.ApplyStatus(ctx, res)
	require.Zero(t, h.writes)
	require.Equal(t, 1, h.requeues)

	// The reduction converges: desired now carries our parent (with a fresh reason, so the
	// follow-up pass has something to write).
	converged := ourParent()
	converged.Conditions[0].Reason = string(gwv1.RouteReasonResolvedRefs)
	h.desired = func() gwv1.RouteStatus {
		return gwv1.RouteStatus{Parents: []gwv1.RouteParentStatus{converged}}
	}

	h.writer.ApplyStatus(ctx, res)
	require.Equal(t, 1, h.writes, "the follow-up pass must refresh the status, not clear it")
	require.True(t, OwnsAnyRouteParent(deferTestController, h.live.Status.Parents),
		"our parent must survive the deferred pass")

	// A later, genuine retraction is a fresh episode: it must defer once again rather than
	// clear immediately off a stale mark.
	h.desired = func() gwv1.RouteStatus { return gwv1.RouteStatus{} }
	h.writer.ApplyStatus(ctx, res)
	require.Equal(t, 1, h.writes, "a fresh retraction episode must defer on its first look")
	require.Equal(t, 2, h.requeues)
	h.writer.ApplyStatus(ctx, res)
	require.Equal(t, 2, h.writes, "and clear on its follow-up pass")
	require.False(t, OwnsAnyRouteParent(deferTestController, h.live.Status.Parents))
}

// TestWriterPartialRetractionIsNotDeferred: losing some parents while keeping others means
// the reduction is non-empty — the pipeline has spoken — so the write goes out immediately.
func TestWriterPartialRetractionIsNotDeferred(t *testing.T) {
	h := newDeferHarness()
	ctx := context.Background()

	second := ourParent()
	second.ParentRef.Name = "gw-2"
	h.live.Status.Parents = append(h.live.Status.Parents, second)

	// Desired keeps one of our two parents: a partial update, not a pure retraction.
	h.desired = func() gwv1.RouteStatus {
		return gwv1.RouteStatus{Parents: []gwv1.RouteParentStatus{ourParent()}}
	}

	h.writer.ApplyStatus(ctx, deferTestResource())
	require.Equal(t, 1, h.writes, "a partial update must write immediately")
	require.Zero(t, h.requeues)
}
