package testutils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// GetAssetsDir returns the envtest binary assets directory.
// It first checks KUBEBUILDER_ASSETS env var, then falls back to running `make envtest-path`.
func GetAssetsDir() (string, error) {
	var assets string
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// set default if not user provided
		out, err := exec.Command("sh", "-c", "make -s --no-print-directory -C $(dirname $(go env GOMOD)) envtest-path").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to get assets dir: %w (output: %s)", err, string(out))
		}
		assets = strings.TrimSpace(string(out))
	}
	if assets != "" {
		info, err := os.Stat(assets)
		if err != nil {
			return "", fmt.Errorf("assets directory does not exist: %s: %w", assets, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("assets path is not a directory: %s", assets)
		}
	}
	return assets, nil
}

// DefaultCRDDirs returns the standard CRD directories for envtest.
// This includes kgateway CRDs, agentgateway CRDs, and Gateway API CRDs.
func DefaultCRDDirs() ([]string, error) {
	gitRoot := GitRootDirectory()
	dirs := []string{
		filepath.Join(gitRoot, CRDPath),
		filepath.Join(gitRoot, AgwCRDPath),
	}

	// Add Gateway API CRDs from the go module
	gwapiDir, err := GetGatewayAPICRDDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get Gateway API CRD directory: %w", err)
	}
	dirs = append(dirs, gwapiDir)

	return dirs, nil
}

// GenerateKubeConfiguration creates a kubeconfig file from a rest.Config.
// The file is created in t.TempDir() and will be automatically cleaned up.
func GenerateKubeConfiguration(t *testing.T, restconfig *rest.Config) string {
	clusters := make(map[string]*clientcmdapi.Cluster)
	authinfos := make(map[string]*clientcmdapi.AuthInfo)
	contexts := make(map[string]*clientcmdapi.Context)

	clusterName := "cluster"
	clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   restconfig.Host,
		CertificateAuthorityData: restconfig.CAData,
	}
	authinfos[clusterName] = &clientcmdapi.AuthInfo{
		ClientKeyData:         restconfig.KeyData,
		ClientCertificateData: restconfig.CertData,
	}
	contexts[clusterName] = &clientcmdapi.Context{
		Cluster:   clusterName,
		Namespace: "default",
		AuthInfo:  clusterName,
	}

	clientConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters:   clusters,
		Contexts:   contexts,
		// current context must be mgmt cluster for now, as the api server doesn't have context configurable.
		CurrentContext: "cluster",
		AuthInfos:      authinfos,
	}
	// create temp file
	tmpfile := filepath.Join(t.TempDir(), "kubeconfig")
	err := clientcmd.WriteToFile(clientConfig, tmpfile)
	if err != nil {
		t.Fatalf("failed to write kubeconfig: %v", err)
	}

	return tmpfile
}
