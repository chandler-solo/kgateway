package agentgatewaysyncer

import "os"

// k8s.io/client-go 0.35 turned WatchListClient on by default. Tests here use
// istio.io/istio/pkg/kube's fake client (still pinned at k8s 0.34), which does
// not emit the Bookmark event the new reflector requires to mark the initial
// list complete. Without this, informers never sync and tests hang forever.
// Remove once istio.io/istio is bumped to a k8s 0.35-compatible version.
func init() {
	os.Setenv("KUBE_FEATURE_WatchListClient", "false")
}
