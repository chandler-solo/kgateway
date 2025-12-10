package wellknown

import "testing"

func TestIsAgwControllerName(t *testing.T) {
	tests := []struct {
		name           string
		controllerName string
		want           bool
	}{
		{
			name:           "default agentgateway controller name (agentgateway.dev/agentgateway)",
			controllerName: DefaultAgwControllerName,
			want:           true,
		},
		{
			name:           "legacy agentgateway controller name (kgateway.dev/agentgateway)",
			controllerName: LegacyAgwControllerName,
			want:           true,
		},
		{
			name:           "agentgateway.dev/agentgateway literal",
			controllerName: "agentgateway.dev/agentgateway",
			want:           true,
		},
		{
			name:           "kgateway.dev/agentgateway literal",
			controllerName: "kgateway.dev/agentgateway",
			want:           true,
		},
		{
			name:           "envoy controller name",
			controllerName: DefaultGatewayControllerName,
			want:           false,
		},
		{
			name:           "kgateway.dev/kgateway literal",
			controllerName: "kgateway.dev/kgateway",
			want:           false,
		},
		{
			name:           "empty string",
			controllerName: "",
			want:           false,
		},
		{
			name:           "other controller",
			controllerName: "example.com/my-controller",
			want:           false,
		},
		{
			name:           "similar but not matching - agentgateway.dev/kgateway",
			controllerName: "agentgateway.dev/kgateway",
			want:           false,
		},
		{
			name:           "similar but not matching - kgateway.dev/agent",
			controllerName: "kgateway.dev/agent",
			want:           false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAgwControllerName(tt.controllerName); got != tt.want {
				t.Errorf("IsAgwControllerName(%q) = %v, want %v", tt.controllerName, got, tt.want)
			}
		})
	}
}

func TestDefaultAgwControllerNameValue(t *testing.T) {
	// Verify the default is agentgateway.dev/agentgateway
	if DefaultAgwControllerName != "agentgateway.dev/agentgateway" {
		t.Errorf("DefaultAgwControllerName = %q, want %q", DefaultAgwControllerName, "agentgateway.dev/agentgateway")
	}
}

func TestLegacyAgwControllerNameValue(t *testing.T) {
	// Verify the legacy name is kgateway.dev/agentgateway
	if LegacyAgwControllerName != "kgateway.dev/agentgateway" {
		t.Errorf("LegacyAgwControllerName = %q, want %q", LegacyAgwControllerName, "kgateway.dev/agentgateway")
	}
}
