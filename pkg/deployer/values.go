package deployer

// HelmPort represents a Gateway Listener port (shared by Envoy and Agentgateway).
type HelmPort struct {
	Port       *int32  `json:"port,omitempty"`
	Protocol   *string `json:"protocol,omitempty"`
	Name       *string `json:"name,omitempty"`
	TargetPort *int32  `json:"targetPort,omitempty"`
	NodePort   *int32  `json:"nodePort,omitempty"`
}

// HelmImage represents container image configuration (shared by Envoy and Agentgateway).
type HelmImage struct {
	Registry   *string `json:"registry,omitempty"`
	Repository *string `json:"repository,omitempty"`
	Tag        *string `json:"tag,omitempty"`
	Digest     *string `json:"digest,omitempty"`
	PullPolicy *string `json:"pullPolicy,omitempty"`
}

// HelmServiceAccount represents service account configuration (shared by Envoy and Agentgateway).
type HelmServiceAccount struct {
	ExtraAnnotations map[string]string `json:"extraAnnotations,omitempty"`
	ExtraLabels      map[string]string `json:"extraLabels,omitempty"`
}

// HelmXds represents xds host and port configuration (shared by Envoy and Agentgateway).
type HelmXds struct {
	Host *string     `json:"host,omitempty"`
	Port *uint32     `json:"port,omitempty"`
	Tls  *HelmXdsTls `json:"tls,omitempty"`
}

// HelmXdsTls represents xds TLS configuration.
type HelmXdsTls struct {
	Enabled *bool   `json:"enabled,omitempty"`
	CaCert  *string `json:"caCert,omitempty"`
}
