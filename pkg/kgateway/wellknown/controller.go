package wellknown

const (
	// DefaultGatewayClassName represents the name of the GatewayClass to watch for
	DefaultGatewayClassName = "kgateway"

	// DefaultWaypointClassName is the GatewayClass name for the waypoint.
	DefaultWaypointClassName = "kgateway-waypoint"

	// DefaultAgwClassName is the GatewayClass name for the agentgateway proxy.
	DefaultAgwClassName = "agentgateway"

	// DefaultGatewayControllerName is the name of the controller that has implemented the Gateway API
	// It is configured to manage GatewayClasses with the name DefaultGatewayClassName
	DefaultGatewayControllerName = "kgateway.dev/kgateway"

	// DefaultAgwControllerName is the name of the agentgateway controller that has implemented the Gateway API
	// It is configured to manage GatewayClasses with the name DefaultGatewayClassName
	DefaultAgwControllerName = "agentgateway.dev/agentgateway"

	// LegacyAgwControllerName is the legacy controller name for agentgateway that is still supported
	// for backwards compatibility. Users can use either this or DefaultAgwControllerName.
	LegacyAgwControllerName = "kgateway.dev/agentgateway"

	// DefaultGatewayParametersName is the name of the GatewayParameters which is attached by
	// parametersRef to the GatewayClass.
	DefaultGatewayParametersName = "kgateway"

	// GatewayNameLabel is a label on GW pods to indicate the name of the gateway
	// they are associated with.
	GatewayNameLabel = "gateway.networking.k8s.io/gateway-name"
	// GatewayClassNameLabel is a label on GW pods to indicate the name of the GatewayClass
	// they are associated with.
	GatewayClassNameLabel = "gateway.networking.k8s.io/gateway-class-name"

	// LeaderElectionID is the name of the lease that leader election will use for holding the leader lock.
	LeaderElectionID = "kgateway"
)

// IsAgwControllerName returns true if the given controller name is an agentgateway controller.
// This supports both the default controller name (kgateway.dev/agentgateway) and the legacy
// controller name (agentgateway.dev/agentgateway) for backwards compatibility.
func IsAgwControllerName(controllerName string) bool {
	return controllerName == DefaultAgwControllerName || controllerName == LegacyAgwControllerName
}
