package v1alpha1

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestProxyDeployment_ValidatePodDisruptionBudget(t *testing.T) {
	tests := []struct {
		name    string
		pdb     *runtime.RawExtension
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil ProxyDeployment",
			pdb:     nil,
			wantErr: false,
		},
		{
			name:    "nil PodDisruptionBudget",
			pdb:     nil,
			wantErr: false,
		},
		{
			name:    "empty PodDisruptionBudget",
			pdb:     &runtime.RawExtension{Raw: []byte("{}")},
			wantErr: false, // Empty PDB is valid - official validator doesn't require minAvailable/maxUnavailable
		},
		{
			name: "valid PodDisruptionBudget with minAvailable",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
				}),
			},
			wantErr: false,
		},
		{
			name: "valid PodDisruptionBudget with maxUnavailable",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"maxUnavailable": 1,
				}),
			},
			wantErr: false,
		},
		{
			name: "valid PodDisruptionBudget with minAvailable percentage",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": "100%",
				}),
			},
			wantErr: false,
		},
		{
			name: "valid PodDisruptionBudget with all fields",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector": map[string]any{
						"matchLabels": map[string]string{
							"app": "test",
						},
					},
					"unhealthyPodEvictionPolicy": "IfHealthyBudget",
				}),
			},
			wantErr: false,
		},
		{
			name: "invalid PodDisruptionBudget with arbitrary extra fields",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"butAny": map[string]any{
						"keyWill": map[string]string{
							"workInA": "RawExtension",
						},
					},
				}),
			},
			wantErr: true,
			errMsg:  "unknown field",
		},
		{
			name: "invalid PodDisruptionBudget with arbitrary selector fields",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": "100%",
					"selector": map[string]any{
						// matchLabels / matchExpressions is missing:
						"anything/at/all": "arbitrary-selector-data",
					},
				}),
			},
			wantErr: true,
			errMsg:  "unknown field",
		},
		{
			name: "invalid JSON",
			pdb: &runtime.RawExtension{
				Raw: []byte(`{"minAvailable": `),
			},
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name: "valid PodDisruptionBudget with empty selector",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector":     map[string]any{},
				}),
			},
			wantErr: false,
		},
		{
			name: "valid PodDisruptionBudget with matchExpressions",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector": map[string]any{
						"matchExpressions": []any{
							map[string]any{
								"key":      "app",
								"operator": "In",
								"values":   []string{"web", "api"},
							},
						},
					},
				}),
			},
			wantErr: false,
		},
		{
			name: "invalid PodDisruptionBudget - selector is not an object",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector":     "not-an-object",
				}),
			},
			wantErr: true,
			errMsg:  "cannot unmarshal string into Go struct field PodDisruptionBudgetSpec.selector",
		},
		{
			name: "invalid PodDisruptionBudget - matchLabels is not a map",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector": map[string]any{
						"matchLabels": "not-a-map",
					},
				}),
			},
			wantErr: true,
			errMsg:  "cannot unmarshal string into Go struct field LabelSelector.selector.matchLabels",
		},
		{
			name: "invalid PodDisruptionBudget - matchExpressions is not an array",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"selector": map[string]any{
						"matchExpressions": "not-an-array",
					},
				}),
			},
			wantErr: true,
			errMsg:  "cannot unmarshal string into Go struct field LabelSelector.selector.matchExpressions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := &ProxyDeployment{
				PodDisruptionBudget: tt.pdb,
			}
			err := pd.ValidatePodDisruptionBudget()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePodDisruptionBudget() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidatePodDisruptionBudget() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestProxyDeployment_GetPodDisruptionBudget(t *testing.T) {
	tests := []struct {
		name    string
		pdb     *runtime.RawExtension
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "nil PodDisruptionBudget",
			pdb:     nil,
			want:    nil,
			wantErr: false,
		},
		{
			name: "valid PodDisruptionBudget",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"unhealthyPodEvictionPolicy": "AlwaysAllow",
				}),
			},
			want: map[string]any{
				"minAvailable": float64(1), // JSON unmarshals numbers as float64
				"unhealthyPodEvictionPolicy": "AlwaysAllow",
			},
			wantErr: false,
		},
		{
			name: "invalid PodDisruptionBudget with extra fields",
			pdb: &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": 1,
					"unknownField": "value",
				}),
			},
			want:    nil,
			wantErr: true, // Extra fields are rejected by strict validation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := &ProxyDeployment{
				PodDisruptionBudget: tt.pdb,
			}
			got, err := pd.GetPodDisruptionBudget()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetPodDisruptionBudget() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if tt.want == nil && got != nil {
					t.Errorf("GetPodDisruptionBudget() = %v, want %v", got, tt.want)
				}
				if tt.want != nil {
					// Compare the map values
					if len(got) != len(tt.want) {
						t.Errorf("GetPodDisruptionBudget() length = %v, want %v", len(got), len(tt.want))
					}
					for k, v := range tt.want {
						if got[k] != v {
							t.Errorf("GetPodDisruptionBudget()[%q] = %v, want %v", k, got[k], v)
						}
					}
				}
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	return data
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Test that validates the IntOrString type works correctly
func TestProxyDeployment_ValidatePodDisruptionBudget_IntOrString(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantErr bool
	}{
		{
			name:    "integer minAvailable",
			value:   1,
			wantErr: false,
		},
		{
			name:    "percentage string minAvailable",
			value:   "50%",
			wantErr: false,
		},
		{
			name:    "100 percentage minAvailable",
			value:   "100%",
			wantErr: false,
		},
		{
			name:    "0 minAvailable",
			value:   0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pdb := &runtime.RawExtension{
				Raw: mustMarshal(t, map[string]any{
					"minAvailable": tt.value,
				}),
			}

			pd := &ProxyDeployment{
				PodDisruptionBudget: pdb,
			}

			err := pd.ValidatePodDisruptionBudget()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePodDisruptionBudget() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestPodDisruptionBudget_UserExperience demonstrates real-world user scenarios and error messages
// These tests show what users will experience when configuring PodDisruptionBudget
func TestPodDisruptionBudget_UserExperience(t *testing.T) {
	t.Run("User configures valid PDB with minAvailable", func(t *testing.T) {
		// User wants to ensure at least 1 pod is always available during disruptions
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable": 1,
							}),
						},
					},
				},
			},
		}

		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("User's valid configuration was rejected: %v", err)
		}
		if pdb["minAvailable"] != float64(1) {
			t.Errorf("Expected minAvailable=1, got %v", pdb["minAvailable"])
		}
		t.Logf("User successfully configured PDB with minAvailable=1")
	})

	t.Run("User configures valid PDB with percentage", func(t *testing.T) {
		// User wants to ensure 80% of pods are always available
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable": "80%",
							}),
						},
					},
				},
			},
		}

		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("User's valid configuration was rejected: %v", err)
		}
		if pdb["minAvailable"] != "80%" {
			t.Errorf("Expected minAvailable=80%%, got %v", pdb["minAvailable"])
		}
		t.Logf("User successfully configured PDB with minAvailable=80%%")
	})

	t.Run("User makes typo in field name", func(t *testing.T) {
		// User accidentally types "minAvailble" instead of "minAvailable"
		// This will be caught by strict validation as an unknown field
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailble": 1, // typo: should be "minAvailable"
							}),
						},
					},
				},
			},
		}

		_, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err == nil {
			t.Fatal("Expected error when user makes typo in field name, got nil")
		}

		// User should see error about unknown field
		if !contains(err.Error(), "unknown field") {
			t.Errorf("Expected error about unknown field, got: %v", err)
		} else {
			t.Logf("User sees helpful error: %v", err)
		}
	})

	t.Run("User adds extra fields (rejected by strict validation)", func(t *testing.T) {
		// User adds extra fields - these are rejected by strict validation
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable":   1,
								"customMetadata": map[string]string{ // Extra field - rejected
									"env": "production",
								},
							}),
						},
					},
				},
			},
		}

		_, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err == nil {
			t.Fatal("Expected error when user adds unknown fields, got nil")
		}
		if !contains(err.Error(), "unknown field") {
			t.Errorf("Expected error about unknown field, got: %v", err)
		} else {
			t.Logf("User sees validation error for unknown fields: %v", err)
		}
	})

	t.Run("User can omit minAvailable and maxUnavailable", func(t *testing.T) {
		// User creates PDB config without minAvailable/maxUnavailable
		// This is valid - the official validator doesn't require these fields
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"selector": map[string]any{
									"matchLabels": map[string]string{
										"app": "myapp",
									},
								},
							}),
						},
					},
				},
			},
		}

		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("Unexpected error for PDB without minAvailable/maxUnavailable: %v", err)
		}
		selector, ok := pdb["selector"].(map[string]any)
		if !ok {
			t.Fatal("Expected selector to be present")
		}
		matchLabels, ok := selector["matchLabels"].(map[string]any)
		if !ok || matchLabels["app"] != "myapp" {
			t.Errorf("Expected matchLabels.app=myapp, got %v", matchLabels)
		}
		t.Logf("✓ User can create PDB without minAvailable/maxUnavailable (official validator allows this)")
	})

	t.Run("User configures valid PDB with all supported fields", func(t *testing.T) {
		// User wants a complete PDB configuration
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable": 2,
								"selector": map[string]any{
									"matchLabels": map[string]string{
										"app":  "gateway",
										"tier": "production",
									},
								},
								"unhealthyPodEvictionPolicy": "IfHealthyBudget",
							}),
						},
					},
				},
			},
		}

		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("User's complete valid configuration was rejected: %v", err)
		}
		if pdb["minAvailable"] != float64(2) {
			t.Errorf("Expected minAvailable=2, got %v", pdb["minAvailable"])
		}
		if pdb["unhealthyPodEvictionPolicy"] != "IfHealthyBudget" {
			t.Errorf("Expected unhealthyPodEvictionPolicy=IfHealthyBudget, got %v", pdb["unhealthyPodEvictionPolicy"])
		}
		t.Logf("✓ User successfully configured complete PDB with all fields")
	})

	t.Run("User copies example with maxUnavailable", func(t *testing.T) {
		// User copies an example using maxUnavailable instead of minAvailable
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"maxUnavailable": "25%",
							}),
						},
					},
				},
			},
		}

		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("User's valid maxUnavailable configuration was rejected: %v", err)
		}
		if pdb["maxUnavailable"] != "25%" {
			t.Errorf("Expected maxUnavailable=25%%, got %v", pdb["maxUnavailable"])
		}
		t.Logf("✓ User successfully configured PDB with maxUnavailable=25%%")
	})

	t.Run("User includes arbitrary fields (rejected by strict validation)", func(t *testing.T) {
		// User includes arbitrary fields - rejected by strict validation
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable": 1,
								"replicas":     3, // Extra field - rejected
							}),
						},
					},
				},
			},
		}

		_, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err == nil {
			t.Fatal("Expected error for arbitrary fields, got nil")
		}
		if !contains(err.Error(), "unknown field") {
			t.Errorf("Expected error about unknown field, got: %v", err)
		} else {
			t.Logf("User sees validation error for unknown fields: %v", err)
		}
	})

	t.Run("User tries to set both minAvailable and maxUnavailable", func(t *testing.T) {
		// User tries to set both fields (which is actually valid in k8s PDB spec)
		gwp := &GatewayParameters{
			Spec: GatewayParametersSpec{
				Kube: &KubernetesProxyConfig{
					Deployment: &ProxyDeployment{
						PodDisruptionBudget: &runtime.RawExtension{
							Raw: mustMarshal(t, map[string]any{
								"minAvailable":   2,
								"maxUnavailable": 1,
							}),
						},
					},
				},
			},
		}

		// This should actually succeed - k8s allows both, though only one will be used
		pdb, err := gwp.Spec.Kube.Deployment.GetPodDisruptionBudget()
		if err != nil {
			t.Fatalf("Unexpected error when both fields are set: %v", err)
		}
		if pdb["minAvailable"] != float64(2) || pdb["maxUnavailable"] != float64(1) {
			t.Errorf("Expected both fields to be preserved, got minAvailable=%v, maxUnavailable=%v",
				pdb["minAvailable"], pdb["maxUnavailable"])
		}
		t.Logf("✓ User can set both minAvailable and maxUnavailable (k8s will use one)")
	})
}

// TestPodDisruptionBudget_ErrorMessages verifies that error messages are
// user-friendly. TODO(chandler): To be truly user-friendly, we must reject an
// invalid GatewayParameters at admission time with a validating admission
// webhook. CEL has complexity limits that make it hard, if not impossible, to
// use it to validate a RawExtension.
func TestPodDisruptionBudget_ErrorMessages(t *testing.T) {
	tests := []struct {
		name          string
		pdbConfig     map[string]any
		rawJSON       []byte // For malformed JSON
		expectedError string
		description   string
	}{
		{
			name:          "unknown field shows helpful error",
			pdbConfig:     map[string]any{"unknownField": "value"},
			expectedError: "unknown field",
			description:   "Error message should indicate unknown field",
		},
		{
			name:          "malformed JSON shows parse error",
			rawJSON:       []byte(`{"minAvailable": "not a valid value"`),
			expectedError: "invalid",
			description:   "Error message should indicate JSON parsing issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			if tt.rawJSON != nil {
				raw = tt.rawJSON
			} else {
				raw = mustMarshal(t, tt.pdbConfig)
			}

			pd := &ProxyDeployment{
				PodDisruptionBudget: &runtime.RawExtension{Raw: raw},
			}

			_, err := pd.GetPodDisruptionBudget()
			if err == nil {
				t.Fatalf("%s: Expected error, got nil", tt.description)
			}
			if !contains(err.Error(), tt.expectedError) {
				t.Errorf("%s\nExpected error containing %q\nGot: %v",
					tt.description, tt.expectedError, err)
			} else {
				t.Logf("✓ %s: %v", tt.description, err)
			}
		})
	}
}
