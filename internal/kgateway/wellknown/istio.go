package wellknown

import (
	"k8s.io/apimachinery/pkg/util/sets"

	istionetworking "istio.io/client-go/pkg/apis/networking/v1"
)

var (
	ServiceEntryGVK = istionetworking.SchemeGroupVersion.WithKind("ServiceEntry")
	HostnameGVK     = istionetworking.SchemeGroupVersion.WithKind("Hostname")
)

var (
	GlobalRefGKs = sets.New(
		HostnameGVK.GroupKind(),
	)
)
