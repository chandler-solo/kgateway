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
type KubernetesResourceOverlay struct {
	// metadata defines a subset of object metadata to be customized.
	// +optional
	Metadata *ObjectMetadata `json:"metadata,omitempty"`

	// Spec provides an opaque mechanism to configure the resource Spec.
	// This field accepts a complete or partial Kubernetes resource spec (e.g., PodSpec, ServiceSpec)
	// and will be merged with the generated configuration using **Strategic Merge Patch** semantics.
	// The patch is applied after all other fields are applied.
	// If you merge-patch the same resource from parameters on the
	// GatewayClass and also from parameters on the Gateway, then
	// the GatewayClass merge-patch happens first.
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
