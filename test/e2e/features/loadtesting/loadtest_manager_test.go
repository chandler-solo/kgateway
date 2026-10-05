//go:build e2e

package loadtesting

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/testutils/cluster"
)

func TestGatewayReadinessWaitsForProxy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gwv1.Gateway, *appsv1.Deployment)
		absent bool
		ready  bool
		detail string
	}{
		{name: "ready proxy", ready: true},
		{name: "deployment not created", absent: true, detail: "proxy deployment read failed"},
		{name: "proxy starting", mutate: func(_ *gwv1.Gateway, d *appsv1.Deployment) {
			d.Status.ReadyReplicas = 0
			d.Status.AvailableReplicas = 0
		}, detail: "proxy deployment not ready"},
		{name: "unobserved rollout", mutate: func(_ *gwv1.Gateway, d *appsv1.Deployment) {
			d.Generation++
		}, detail: "proxy deployment not ready"},
		{name: "old replica still serving", mutate: func(_ *gwv1.Gateway, d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
		}, detail: "proxy deployment not ready"},
		{name: "scaled to zero", mutate: func(_ *gwv1.Gateway, d *appsv1.Deployment) {
			d.Spec.Replicas = ptr.To(int32(0))
			d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation}
		}, detail: "proxy deployment not ready"},
		{name: "listener not programmed", mutate: func(g *gwv1.Gateway, _ *appsv1.Deployment) {
			g.Status.Listeners[0].Conditions[0].Status = metav1.ConditionFalse
		}, detail: "no Programmed listener"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &gwv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "test"},
				Status: gwv1.GatewayStatus{Listeners: []gwv1.ListenerStatus{{
					Name: "http",
					Conditions: []metav1.Condition{{
						Type: string(gwv1.ListenerConditionProgrammed), Status: metav1.ConditionTrue,
					}},
				}}},
			}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: gateway.Name, Namespace: gateway.Namespace, Generation: 1},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
				},
			}
			if tc.mutate != nil {
				tc.mutate(gateway, deployment)
			}
			scheme := runtime.NewScheme()
			require.NoError(t, gwv1.Install(scheme))
			require.NoError(t, appsv1.AddToScheme(scheme))
			objects := []client.Object{gateway}
			if !tc.absent {
				objects = append(objects, deployment)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			manager := NewLoadTestManager(context.Background(), &e2e.TestInstallation{
				ClusterContext: &cluster.Context{Client: kube},
			}, gateway.Namespace)

			ready, detail := manager.gatewayReadiness(gateway)
			assert.Equal(t, tc.ready, ready)
			if tc.ready {
				assert.Empty(t, detail)
			} else {
				assert.Contains(t, detail, tc.detail)
			}
		})
	}
}
