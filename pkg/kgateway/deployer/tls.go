package deployer

import (
	"fmt"
	"os"

	"k8s.io/utils/ptr"

	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
)

// injectXdsCACertificate reads the CA certificate from the control plane's
// mounted TLS Secret and injects it into the Helm values (xdsConfig) so it can
// be used by the proxy templates.
func injectXdsCACertificate(caCertPath string, xdsConfig *deployer.HelmXds) error {
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

	if xdsConfig != nil && xdsConfig.Tls != nil {
		xdsConfig.Tls.CaCert = ptr.To(string(caCert))
	}

	return nil
}
