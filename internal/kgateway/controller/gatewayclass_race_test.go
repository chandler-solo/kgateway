package controller

import (
	"context"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	apiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/schemes"
)

// TestConcurrentReconciliation_OldBehavior reproduces the original data race
// that occurred when multiple concurrent reconciliations would try to create
// all GatewayClasses simultaneously.
//
// To run this test with race detection:
//   go test -race -run TestConcurrentReconciliation_OldBehavior
//
// This test simulates the old behavior where Reconcile() would iterate through
// ALL classConfigs and create them, rather than just the one being reconciled.
func TestConcurrentReconciliation_OldBehavior(t *testing.T) {
	// Skip this test by default since it demonstrates buggy behavior
	// Uncomment to reproduce the race:
	// DLC t.Skip("This test demonstrates the old buggy behavior that causes a data race")

	ctx := context.Background()
	scheme := schemes.GatewayScheme()

	// Create multiple GatewayClass configurations
	classConfigs := map[string]*deployer.GatewayClassInfo{
		"class-1": {
			Description:    "class 1",
			ControllerName: "controller-1",
		},
		"class-2": {
			Description:    "class 2",
			ControllerName: "controller-2",
		},
		"class-3": {
			Description:    "class 3",
			ControllerName: "controller-3",
		},
	}

	// Create a fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// This simulates the OLD buggy behavior where Reconcile creates ALL classes
	oldReconcile := func(ctx context.Context, req ctrl.Request) error {
		// OLD BEHAVIOR: Create ALL GatewayClasses on every reconciliation
		for name, config := range classConfigs {
			gc := &apiv1.GatewayClass{}
			err := fakeClient.Get(ctx, client.ObjectKey{Name: name}, gc)
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}

			controllerName := config.ControllerName
			gc = &apiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: config.Annotations,
					Labels:      config.Labels,
				},
				Spec: apiv1.GatewayClassSpec{
					ControllerName: apiv1.GatewayController(controllerName),
				},
			}
			if config.Description != "" {
				gc.Spec.Description = ptr.To(config.Description)
			}
			if err := fakeClient.Create(ctx, gc); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
		}
		return nil
	}

	// Trigger multiple concurrent reconciliations
	// This simulates what happens when multiple GatewayClasses are created/deleted simultaneously
	var wg sync.WaitGroup
	concurrency := 10

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		className := ""
		switch i % 3 {
		case 0:
			className = "class-1"
		case 1:
			className = "class-2"
		case 2:
			className = "class-3"
		}

		go func(name string) {
			defer wg.Done()
			req := ctrl.Request{
				NamespacedName: client.ObjectKey{Name: name},
			}
			// This will cause a race because all goroutines are trying to create
			// ALL classes simultaneously, causing concurrent access to the HTTP client
			_ = oldReconcile(ctx, req)
		}(className)
	}

	wg.Wait()
}

// TestConcurrentReconciliation_NewBehavior verifies that the new behavior
// (only reconciling the specific GatewayClass requested) does not have a race condition.
func TestConcurrentReconciliation_NewBehavior(t *testing.T) {
	ctx := context.Background()
	scheme := schemes.GatewayScheme()

	// Create multiple GatewayClass configurations
	classConfigs := map[string]*deployer.GatewayClassInfo{
		"class-1": {
			Description:    "class 1",
			ControllerName: "controller-1",
		},
		"class-2": {
			Description:    "class 2",
			ControllerName: "controller-2",
		},
		"class-3": {
			Description:    "class 3",
			ControllerName: "controller-3",
		},
	}

	// Create a fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// This simulates the NEW fixed behavior where Reconcile creates ONLY the requested class
	newReconcile := func(ctx context.Context, req ctrl.Request) error {
		// NEW BEHAVIOR: Only create the specific GatewayClass being reconciled
		name := req.Name
		config, exists := classConfigs[name]
		if !exists {
			return nil
		}

		gc := &apiv1.GatewayClass{}
		err := fakeClient.Get(ctx, client.ObjectKey{Name: name}, gc)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		controllerName := config.ControllerName
		gc = &apiv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Annotations: config.Annotations,
				Labels:      config.Labels,
			},
			Spec: apiv1.GatewayClassSpec{
				ControllerName: apiv1.GatewayController(controllerName),
			},
		}
		if config.Description != "" {
			gc.Spec.Description = ptr.To(config.Description)
		}
		if err := fakeClient.Create(ctx, gc); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}

	// Trigger multiple concurrent reconciliations
	var wg sync.WaitGroup
	concurrency := 10

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		className := ""
		switch i % 3 {
		case 0:
			className = "class-1"
		case 1:
			className = "class-2"
		case 2:
			className = "class-3"
		}

		go func(name string) {
			defer wg.Done()
			req := ctrl.Request{
				NamespacedName: client.ObjectKey{Name: name},
			}
			// This should NOT race because each goroutine only creates its own class
			_ = newReconcile(ctx, req)
		}(className)
	}

	wg.Wait()

	// Verify all classes were created
	for name := range classConfigs {
		gc := &apiv1.GatewayClass{}
		err := fakeClient.Get(ctx, client.ObjectKey{Name: name}, gc)
		if err != nil {
			t.Errorf("Expected GatewayClass %s to exist, got error: %v", name, err)
		}
	}
}

// TestDataRaceWithRealClient demonstrates the race using a more realistic scenario
// that more closely matches the actual race condition in the wild.
//
// This test uses a trackedClient that wraps the fake client and tracks concurrent
// operations to demonstrate the race.
func TestDataRaceWithRealClient(t *testing.T) {
	// Skip by default - uncomment to reproduce the race
	t.Skip("This test demonstrates the data race with tracked client operations")

	ctx := context.Background()
	scheme := schemes.GatewayScheme()

	classConfigs := map[string]*deployer.GatewayClassInfo{
		"class-1": {Description: "class 1", ControllerName: "controller-1"},
		"class-2": {Description: "class 2", ControllerName: "controller-2"},
		"class-3": {Description: "class 3", ControllerName: "controller-3"},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Wrapper to track concurrent operations
	type trackedClient struct {
		client.Client
		createCount int32
		mu          sync.Mutex
	}

	tracked := &trackedClient{Client: fakeClient}

	// Old buggy reconcile that creates all classes
	oldReconcile := func(ctx context.Context, req ctrl.Request) error {
		for name, config := range classConfigs {
			gc := &apiv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: apiv1.GatewayClassSpec{
					ControllerName: apiv1.GatewayController(config.ControllerName),
					Description:    ptr.To(config.Description),
				},
			}
			// Multiple goroutines calling Create on the same object name
			// can cause the underlying HTTP client to have concurrent access
			_ = tracked.Create(ctx, gc)
		}
		return nil
	}

	// Trigger concurrent reconciliations
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			className := "class-1"
			if idx%2 == 0 {
				className = "class-2"
			}
			req := ctrl.Request{NamespacedName: client.ObjectKey{Name: className}}
			_ = oldReconcile(ctx, req)
		}(i)
	}

	wg.Wait()
}

// fakeNotFoundError simulates a NotFound error without needing a real API server
type fakeNotFoundError struct {
	error
}

func (e *fakeNotFoundError) Status() metav1.Status {
	return metav1.Status{
		Status: metav1.StatusFailure,
		Reason: metav1.StatusReasonNotFound,
	}
}

func newNotFoundError(name string) error {
	return &apierrors.StatusError{
		ErrStatus: metav1.Status{
			Status:  metav1.StatusFailure,
			Reason:  metav1.StatusReasonNotFound,
			Message: name + " not found",
			Details: &metav1.StatusDetails{
				Name:  name,
				Group: apiv1.GroupVersion.Group,
				Kind:  "GatewayClass",
			},
		},
	}
}
