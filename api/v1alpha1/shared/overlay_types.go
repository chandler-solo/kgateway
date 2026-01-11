package shared

import apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

// ObjectMetadata contains labels and annotations for metadata overlays.
type ObjectMetadata struct {
	// Map of string keys and values that can be used to organize and categorize
	// (scope and select) objects. May match selectors of replication controllers
	// and services.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// KubernetesResourceOverlay provides a mechanism to customize generated
// Kubernetes resources using [Strategic Merge
// Patch](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/strategic-merge-patch.md)
// semantics.
//
// # Overlay Application Order
//
// Overlays are applied **after** all other configuration fields have been processed.
// When the same resource type has overlays defined at multiple levels, they are applied
// in the following order:
//
//  1. Base resource is generated from typed configuration fields (e.g., replicas, image settings)
//  2. GatewayClass-level overlay is applied first (from GatewayClass.spec.parametersRef)
//  3. Gateway-level overlay is applied second (from Gateway.spec.infrastructure.parametersRef)
//
// This ordering means Gateway-level overlays can override values set by GatewayClass-level
// overlays. For example, if both levels set the same label, the Gateway value wins.
type KubernetesResourceOverlay struct {
	// metadata defines a subset of object metadata to be customized.
	// Labels and annotations are merged with existing values. If both GatewayClass
	// and Gateway parameters define the same label or annotation key, the Gateway
	// value takes precedence (applied second).
	// +optional
	Metadata *ObjectMetadata `json:"metadata,omitempty"`

	// Spec provides an opaque mechanism to configure the resource Spec.
	// This field accepts a complete or partial Kubernetes resource spec (e.g., PodSpec, ServiceSpec)
	// and will be merged with the generated configuration using **Strategic Merge Patch** semantics.
	//
	// # Application Order
	//
	// Overlays are applied after all typed configuration fields. When both GatewayClass
	// and Gateway parameters define overlays for the same resource:
	//
	//  1. GatewayClass overlay is applied first
	//  2. Gateway overlay is applied second (can override GatewayClass values)
	//
	// # Strategic Merge Patch & Deletion Guide
	//
	// This merge strategy allows you to override individual fields, merge lists, or delete items
	// without needing to provide the entire resource definition.
	//
	// **1. Replacing Values (Scalars):**
	// Simple fields (strings, integers, booleans) in your config will overwrite the generated defaults.
	//
	// **2. Merging Lists (Append/Merge):**
	// Lists with "merge keys" (like `containers` which merges on `name`, or `tolerations` which merges on `key`)
	// will append your items to the generated list, or update existing items if keys match.
	//
	// **3. Deleting List Items ($patch: delete):**
	// To remove an item from a generated list (e.g., removing a default sidecar), you must use
	// the special `$patch: delete` directive.
	//
	//	spec:
	//	  containers:
	//	    - name: proxy
	//	      # Delete the securityContext using $patch: delete
	//	      securityContext:
	//	        $patch: delete
	//
	// **4. Deleting/Clearing Map Fields (null):**
	// To remove a map field or a scalar entirely, set its value to `null`.
	//
	//	spec:
	//	  template:
	//	    spec:
	//	      nodeSelector: null  # Removes default nodeSelector
	//
	// **5. Replacing Lists Entirely ($patch: replace):**
	// If you want to strictly define a list and ignore all generated defaults, use `$patch: replace`.
	//
	//	service:
	//	  spec:
	//	    ports:
	//	      - $patch: replace
	//	      - name: http
	//	        port: 80
	//	        targetPort: 8080
	//	        protocol: TCP
	//	      - name: https
	//	        port: 443
	//	        targetPort: 8443
	//	        protocol: TCP
	//
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Spec *apiextensionsv1.JSON `json:"spec,omitempty"`
}
