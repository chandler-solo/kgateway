// NOTE(chandler): This is my illuminated manuscript for PR 13007, which is
// much better than the way that kgateway 2.1.0 specifies many Kubernetes
// fields, but never the one you need, in its GatewayParameters.

package agentgateway

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewayparameters,verbs=get;list;watch
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewayparameters/status,verbs=get;update;patch

// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=".status.ancestors[*].conditions[?(@.type=='Accepted')].status",description="Agentgateway policy acceptance status"
// +kubebuilder:printcolumn:name="Attached",type=string,JSONPath=".status.ancestors[*].conditions[?(@.type=='Attached')].status",description="Agentgateway policy attachment status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:metadata:labels={app=kgateway,app.kubernetes.io/name=kgateway}
// +kubebuilder:resource:categories=kgateway,shortName=agpar,path=agentgatewayparameters
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="gateway.networking.k8s.io/policy=Direct"
type AgentgatewayParameters struct {
	metav1.TypeMeta `json:",inline"`
	// metadata for the object
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata"`

	// spec defines the desired state of AgentgatewayParameters.
	// +required
	Spec AgentgatewayParametersSpec `json:"spec"`

	// status defines the current state of AgentgatewayParameters.
	// +optional
	Status AgentgatewayParametersStatus `json:"status"`
}

// The current conditions of the GatewayParameters. This is not currently implemented.
type AgentgatewayParametersStatus struct{}

// +kubebuilder:object:root=true
type AgentgatewayParametersList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []AgentgatewayParameters `json:"items"`
}

type AgentgatewayParametersSpec struct {
	AgentgatewayParametersConfigs  `json:",inline"`
	AgentgatewayParametersOverlays `json:",inline"`
}

// +kubebuilder:validation:Enum=Json;Plain
type AgentgatewayParametersLoggingFormat string

const (
	AgentgatewayParametersLoggingJson  AgentgatewayParametersLoggingFormat = "Json"
	AgentgatewayParametersLoggingPlain AgentgatewayParametersLoggingFormat = "Plain"
)

// +kubebuilder:validation:AtMostOneOf=level;levels
type AgentgatewayParametersLogging struct {
	// +optional
	Level string `json:"level,omitempty"`
	// +optional
	Levels []string `json:"levels,omitempty"`
	// +optional
	Format AgentgatewayParametersLoggingFormat `json:"format,omitempty"`
}

type AgentgatewayParametersConfigs struct {
	// Common set of labels to apply to all generated resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Common set of annotations to apply to all generated resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// logging configuration for Agentgateway. By default, all logs are set to "info" level.
	// +optional
	Logging *AgentgatewayParametersLogging `json:"logging,omitempty"`

	// The agentgateway container image. See
	// https://kubernetes.io/docs/concepts/containers/images
	// for details.
	//
	// Default values, which may be overridden individually:
	//
	//	registry: ghcr.io/agentgateway
	//	repository: agentgateway
	//	tag: <agentgateway version>
	//	pullPolicy: <omitted, relying on Kubernetes defaults which depend on the tag>
	//
	// +optional
	Image *Image `json:"image,omitempty"`

	// The container environment variables. These override any existing
	// values. If you want to delete an environment variable entirely, use
	// `$patch: delete` with AgentgatewayParametersOverlays instead. Note that
	// [variable
	// expansion](https://kubernetes.io/docs/tasks/inject-data-application/define-interdependent-environment-variables/)
	// does apply, but is highly discouraged -- to set dependent environment
	// variables, you can use $(VAR_NAME), but it's highly
	// discouraged. `$$(VAR_NAME)` avoids expansion and results in a literal
	// `$(VAR_NAME)`.
	//
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// The compute resources required by this container. See
	// https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
	// for details.
	//
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

type AgentgatewayParametersOverlays struct {
	// deployment allows specifying overrides for the generated Deployment resource.
	// +optional
	Deployment *KubernetesResourceOverlay `json:"deployment,omitempty"`

	// service allows specifying overrides for the generated Service resource.
	// +optional
	Service *KubernetesResourceOverlay `json:"service,omitempty"`

	// serviceAccount allows specifying overrides for the generated ServiceAccount resource.
	// +optional
	ServiceAccount *KubernetesResourceOverlay `json:"serviceAccount,omitempty"`
}

type AgentgatewayParametersObjectMetadata struct {
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
	Metadata AgentgatewayParametersObjectMetadata `json:"metadata"`

	// Spec provides an opaque mechanism to configure the resource Spec.
	// This field accepts a complete or partial Kubernetes resource spec (e.g., PodSpec, ServiceSpec)
	// and will be merged with the generated configuration using **Strategic Merge Patch** semantics.
	// The patch is applied after all other fields are applied.
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
	//   spec:
	//     template:
	//       spec:
	//         containers:
	//         - name: unwanted-sidecar
	//           $patch: delete
	//
	// **4. Deleting/Clearing Map Fields (null):**
	// To remove a map field or a scalar entirely, set its value to `null`.
	//
	//   spec:
	//     template:
	//       spec:
	//         nodeSelector: null  # Removes default nodeSelector
	//
	// **5. Replacing Lists Entirely ($patch: replace):**
	// If you want to strictly define a list and ignore all generated defaults, use `$patch: replace`.
	//
	//   spec:
	//     template:
	//       spec:
	//         containers:
	//         - name: my-only-container
	//           image: alpine
	//         $patch: replace
	//
	// +optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Spec *apiextensionsv1.JSON `json:"spec,omitempty"`
}

// A container image. See https://kubernetes.io/docs/concepts/containers/images
// for details.
type Image struct {
	// The image registry.
	//
	// +optional
	Registry *string `json:"registry,omitempty"`

	// The image repository (name).
	//
	// +optional
	Repository *string `json:"repository,omitempty"`

	// The image tag.
	//
	// +optional
	Tag *string `json:"tag,omitempty"`

	// The hash digest of the image, e.g. `sha256:12345...`
	//
	// +optional
	Digest *string `json:"digest,omitempty"`

	// The image pull policy for the container. See
	// https://kubernetes.io/docs/concepts/containers/images/#image-pull-policy
	// for details.
	//
	// +optional
	PullPolicy *corev1.PullPolicy `json:"pullPolicy,omitempty"`
}
