//go:build e2e

package deployer

import (
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
)

var (
	// manifests
	agwWithParameters = filepath.Join(fsutils.MustGetThisDir(), "testdata", "agentgateway-with-parameters.yaml")

	// objects
	agwProxyObjectMeta = metav1.ObjectMeta{
		Name:      "agw-deployer-test",
		Namespace: "default",
	}

	agwParamsObjectMeta = metav1.ObjectMeta{
		Name:      "agw-params",
		Namespace: "default",
	}
)
