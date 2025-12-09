package deployer

import (
	"fmt"
	"os"

	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
)

// injectXdsCACertificate reads the CA certificate from the control plane's mounted TLS Secret
// and injects it into the Helm values so it can be used by the proxy templates.
// It accepts variadic HelmXds pointers to support both Envoy (single Xds) and agentgateway (Xds + AgwXds).
func injectXdsCACertificate(caCertPath string, xdsConfigs ...*deployer.HelmXds) error {
	if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
		return fmt.Errorf("xDS TLS is enabled but CA certificate file not found at %s. "+
			"Ensure the xDS TLS secret is properly mounted and contains ca.crt", caCertPath,
		)
	}

	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate from %s: %w", caCertPath, err)
	}
	if len(caCert) == 0 {
		return fmt.Errorf("CA certificate at %s is empty", caCertPath)
	}

	caCertStr := string(caCert)
	for _, xds := range xdsConfigs {
		if xds != nil && xds.Tls != nil {
			xds.Tls.CaCert = &caCertStr
		}
	}

	return nil
}
