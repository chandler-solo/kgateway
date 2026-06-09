package kgateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestProxyDeploymentUnmarshalReplicas verifies that ProxyDeployment.UnmarshalJSON
// distinguishes an explicit `replicas: null` from an omitted `replicas` field,
// which the deployer relies on to decide whether to manage the replica count.
func TestProxyDeploymentUnmarshalReplicas(t *testing.T) {
	cases := []struct {
		name             string
		json             string
		wantReplicas     *int32
		wantExplicitNull bool
	}{
		{
			name:             "omitted",
			json:             `{}`,
			wantReplicas:     nil,
			wantExplicitNull: false,
		},
		{
			name:             "explicit null",
			json:             `{"replicas": null}`,
			wantReplicas:     nil,
			wantExplicitNull: true,
		},
		{
			name:             "explicit value",
			json:             `{"replicas": 3}`,
			wantReplicas:     ptrInt32(3),
			wantExplicitNull: false,
		},
		{
			name:             "explicit zero",
			json:             `{"replicas": 0}`,
			wantReplicas:     ptrInt32(0),
			wantExplicitNull: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pd ProxyDeployment
			require.NoError(t, json.Unmarshal([]byte(tc.json), &pd))
			assert.Equal(t, tc.wantReplicas, pd.GetReplicas())
			assert.Equal(t, tc.wantExplicitNull, pd.IsReplicasExplicitlyNull())
		})
	}
}

// TestProxyDeploymentUnmarshalReplicasYAML exercises the same detection through
// the YAML decode path (sigs.k8s.io/yaml -> JSON), which mirrors how manifests
// are loaded.
func TestProxyDeploymentUnmarshalReplicasYAML(t *testing.T) {
	var explicit ProxyDeployment
	require.NoError(t, yaml.Unmarshal([]byte("replicas: null\n"), &explicit))
	assert.Nil(t, explicit.GetReplicas())
	assert.True(t, explicit.IsReplicasExplicitlyNull(), "replicas: null should be detected as explicit null")

	var omitted ProxyDeployment
	require.NoError(t, yaml.Unmarshal([]byte("strategy: {}\n"), &omitted))
	assert.Nil(t, omitted.GetReplicas())
	assert.False(t, omitted.IsReplicasExplicitlyNull(), "omitted replicas should not be detected as explicit null")
}

// TestProxyDeploymentMarshalReplicas verifies that an explicit null round-trips
// through marshaling (so it survives conversion to unstructured for server-side
// apply) while omitted/valued replicas marshal normally.
func TestProxyDeploymentMarshalReplicas(t *testing.T) {
	t.Run("explicit null re-emitted", func(t *testing.T) {
		pd := ProxyDeployment{ReplicasExplicitlyNull: true}
		data, err := json.Marshal(pd)
		require.NoError(t, err)
		assert.JSONEq(t, `{"replicas": null}`, string(data))

		// Round-trip restores the marker.
		var back ProxyDeployment
		require.NoError(t, json.Unmarshal(data, &back))
		assert.True(t, back.IsReplicasExplicitlyNull())
		assert.Nil(t, back.GetReplicas())
	})

	t.Run("omitted stays omitted", func(t *testing.T) {
		data, err := json.Marshal(ProxyDeployment{})
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, string(data))
	})

	t.Run("explicit value marshals normally", func(t *testing.T) {
		data, err := json.Marshal(ProxyDeployment{Replicas: ptrInt32(3)})
		require.NoError(t, err)
		assert.JSONEq(t, `{"replicas": 3}`, string(data))
	})
}

// TestGatewayParametersUnmarshalNestedReplicasNull verifies the flag survives
// decoding through the full GatewayParameters object.
func TestGatewayParametersUnmarshalNestedReplicasNull(t *testing.T) {
	raw := `{
		"spec": {
			"kube": {
				"deployment": {"replicas": null}
			}
		}
	}`
	var gwp GatewayParameters
	require.NoError(t, json.Unmarshal([]byte(raw), &gwp))
	require.NotNil(t, gwp.Spec.Kube)
	require.NotNil(t, gwp.Spec.Kube.Deployment)
	assert.Nil(t, gwp.Spec.Kube.Deployment.GetReplicas())
	assert.True(t, gwp.Spec.Kube.Deployment.IsReplicasExplicitlyNull())
}

//go:fix inline
func ptrInt32(i int32) *int32 { return new(i) }
