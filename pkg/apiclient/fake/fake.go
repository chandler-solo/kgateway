package fake

import (
	"context"
	"encoding/json"
	"fmt"

	"istio.io/istio/pkg/kube"
	"istio.io/istio/pkg/kube/kclient/clienttest"
	"istio.io/istio/pkg/test"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/gateway-api/pkg/consts"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/agentgateway"
	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/client/clientset/versioned"
	"github.com/kgateway-dev/kgateway/v2/pkg/client/clientset/versioned/fake"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/schemes"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

var _ apiclient.Client = (*cli)(nil)

type cli struct {
	kube.Client
	kgateway versioned.Interface
}

func NewClient(t test.Failer, objects ...client.Object) *cli {
	return NewClientWithExtraGVRs(t, nil, objects...)
}

func NewClientWithExtraGVRs(t test.Failer, extraGVRs []schema.GroupVersionResource, objects ...client.Object) *cli {
	known, kgw := filterObjects(objects...)
	c := &cli{
		Client:   fakeIstioClient(known...),
		kgateway: fakeKgwClient(kgw...),
	}

	allCRDs := append(testutils.AllCRDs, extraGVRs...)
	for _, crd := range allCRDs {
		clienttest.MakeCRDWithAnnotations(t, c.Client, crd, map[string]string{
			consts.BundleVersionAnnotation: consts.BundleVersion,
		})
	}

	apiclient.RegisterTypes()

	return c
}

func (c *cli) Kgateway() versioned.Interface {
	return c.kgateway
}

func (c *cli) Core() kube.Client {
	return c.Client
}

func fakeIstioClient(objects ...client.Object) kube.Client {
	c := kube.NewFakeClient(testutils.ToRuntimeObjects(objects...)...)
	// Also add to the Dynamic store
	for _, obj := range objects {
		nn := kubeutils.NamespacedNameFrom(obj)
		gvr := mustGetGVR(obj, kube.IstioScheme)
		d := c.Dynamic().Resource(gvr).Namespace(obj.GetNamespace())
		us, err := kubeutils.ToUnstructured(obj)
		if err != nil {
			panic(fmt.Sprintf("failed to convert to unstructured for object %T %s: %v", obj, nn, err))
		}
		_, err = d.Create(context.Background(), us, metav1.CreateOptions{})
		if err != nil {
			panic(fmt.Sprintf("failed to create in dynamic client for object %T %s: %v", obj, nn, err))
		}
	}

	return c
}

func fakeKgwClient(objects ...client.Object) *fake.Clientset {
	f := fake.NewSimpleClientset()
	for _, obj := range objects {
		gvr := mustGetGVR(obj, schemes.DefaultScheme())
		// Run Create() instead of Add(), so we can pass the GVR. Otherwise, Kubernetes guesses, and it guesses wrong for 'GatewayParameters'.
		// DeepCopy since it will mutate the managed fields/etc
		objCopy := obj.DeepCopyObject().(client.Object)
		// Strip null values from RawExtension fields to simulate Kubernetes API
		// server behavior. Kubernetes strips nulls from x-kubernetes-preserve-unknown-fields.
		stripNullsFromRawExtensionFields(objCopy)
		if err := f.Tracker().Create(gvr, objCopy, obj.(metav1.ObjectMetaAccessor).GetObjectMeta().GetNamespace()); err != nil {
			panic("failed to create: " + err.Error())
		}
	}
	return f
}

func filterObjects(objects ...client.Object) (istio []client.Object, kgw []client.Object) {
	for _, obj := range objects {
		switch obj.(type) {
		case *kgateway.Backend,
			*kgateway.BackendConfigPolicy,
			*kgateway.DirectResponse,
			*kgateway.GatewayExtension,
			*kgateway.GatewayParameters,
			*kgateway.HTTPListenerPolicy,
			*kgateway.ListenerPolicy,
			*kgateway.TrafficPolicy,
			*agentgateway.AgentgatewayPolicy,
			*agentgateway.AgentgatewayBackend,
			*agentgateway.AgentgatewayParameters:
			kgw = append(kgw, obj)
		default:
			istio = append(istio, obj)
		}
	}
	return istio, kgw
}

func mustGetGVR(obj client.Object, scheme *runtime.Scheme) schema.GroupVersionResource {
	gvr, err := getGVR(obj, scheme)
	if err != nil {
		panic(err)
	}
	return gvr
}

func getGVR(obj client.Object, scheme *runtime.Scheme) (schema.GroupVersionResource, error) {
	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Group == "" {
		gvks, _, _ := scheme.ObjectKinds(obj)
		gvk = gvks[0]
	}
	gvr, err := wellknown.GVKToGVR(gvk)
	if err != nil {
		// try unsafe guess
		gvr, _ = meta.UnsafeGuessKindToResource(gvk)
		if gvr == (schema.GroupVersionResource{}) {
			return schema.GroupVersionResource{}, fmt.Errorf("failed to get GVR for object %s: %v", kubeutils.NamespacedNameFrom(obj), err)
		}
	}
	if gvr.Group == "core" {
		gvr.Group = ""
	}
	return gvr, nil
}

// stripNullsFromRawExtensionFields removes null values from RawExtension/JSON
// fields. This simulates how the Kubernetes API server strips null values from
// x-kubernetes-preserve-unknown-fields when storing CRDs. Without this, test
// files using `securityContext: null` would not correctly simulate production
// behavior where those nulls are stripped before the controller reads the
// resource. (Use `$patch: delete` instead of null to delete fields.)
func stripNullsFromRawExtensionFields(obj client.Object) {
	if agwp, ok := obj.(*agentgateway.AgentgatewayParameters); ok {
		stripNullsFromAGWP(agwp)
	}
}

func stripNullsFromAGWP(agwp *agentgateway.AgentgatewayParameters) {
	if agwp == nil {
		return
	}
	// Strip nulls from overlay Spec fields
	if agwp.Spec.Deployment != nil {
		agwp.Spec.Deployment.Spec = stripNullsFromAPIExtJSON(agwp.Spec.Deployment.Spec)
	}
	if agwp.Spec.Service != nil {
		agwp.Spec.Service.Spec = stripNullsFromAPIExtJSON(agwp.Spec.Service.Spec)
	}
	if agwp.Spec.ServiceAccount != nil {
		agwp.Spec.ServiceAccount.Spec = stripNullsFromAPIExtJSON(agwp.Spec.ServiceAccount.Spec)
	}
	if agwp.Spec.PodDisruptionBudget != nil {
		agwp.Spec.PodDisruptionBudget.Spec = stripNullsFromAPIExtJSON(agwp.Spec.PodDisruptionBudget.Spec)
	}
	if agwp.Spec.HorizontalPodAutoscaler != nil {
		agwp.Spec.HorizontalPodAutoscaler.Spec = stripNullsFromAPIExtJSON(agwp.Spec.HorizontalPodAutoscaler.Spec)
	}
	// Also strip from RawConfig
	agwp.Spec.RawConfig = stripNullsFromAPIExtJSON(agwp.Spec.RawConfig)
}

func stripNullsFromAPIExtJSON(j *apiextensionsv1.JSON) *apiextensionsv1.JSON {
	if j == nil || len(j.Raw) == 0 {
		return j
	}
	stripped := stripNullsFromJSON(j.Raw)
	if len(stripped) == 0 {
		return nil
	}
	return &apiextensionsv1.JSON{Raw: stripped}
}

func stripNullsFromJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return raw
	}
	stripped := stripNullsRecursive(data)
	if stripped == nil {
		return nil
	}
	result, err := json.Marshal(stripped)
	if err != nil {
		return raw
	}
	return result
}

func stripNullsRecursive(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			if v == nil {
				continue // Strip null values
			}
			stripped := stripNullsRecursive(v)
			if stripped != nil {
				result[k] = stripped
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []any:
		var result []any
		for _, item := range val {
			if item == nil {
				continue // Strip null values in arrays
			}
			stripped := stripNullsRecursive(item)
			if stripped != nil {
				result = append(result, stripped)
			}
		}
		return result
	default:
		return v
	}
}
