package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// Render the chart rather than duplicating its timeout and initialization graph.
func renderTimeoutBootstrap(chart, values, output string) error {
	data, err := exec.Command("helm", "template", "formal", chart, "-f", values,
		"--namespace", "formal", "--show-only", "templates/configmap.yaml").CombinedOutput()
	if err != nil {
		return fmt.Errorf("render bootstrap: %w: %s", err, data)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	raw, err := yaml.YAMLToJSON(data)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &cm); err != nil {
		return err
	}
	bootstrap, ok := cm.Data["envoy.yaml"]
	if !ok {
		return errors.New("rendered ConfigMap has no envoy.yaml")
	}
	raw, err = yaml.YAMLToJSON([]byte(bootstrap))
	if err != nil {
		return err
	}
	if err = os.WriteFile(output, raw, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(output), "rendered-configmap.yaml"), data, 0o600)
}

// Replace only probe transport, identity, admin address and the fixture listener.
// Keep the rendered local EDS cluster, cluster manager and CDS/LDS ConfigSources.
func adaptTimeoutBootstrap(rendered string, fixture string, outputDir string) (string, error) {
	raw, err := os.ReadFile(rendered)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(outputDir, "rendered-bootstrap.json"), raw, 0o600); err != nil {
		return "", err
	}
	var base, extra map[string]any
	if err = json.Unmarshal(raw, &base); err != nil {
		return "", err
	}
	if err = json.Unmarshal([]byte(fixture), &extra); err != nil {
		return "", err
	}
	base["admin"], base["node"] = extra["admin"], extra["node"]
	dynamic := base["dynamic_resources"].(map[string]any)
	dynamic["ads_config"] = extra["dynamic_resources"].(map[string]any)["ads_config"]
	statics, fixtureStatics := base["static_resources"].(map[string]any), extra["static_resources"].(map[string]any)
	clusters := []any{}
	for _, item := range statics["clusters"].([]any) {
		cluster := item.(map[string]any)
		if cluster["name"] == "xds_cluster" {
			continue
		}
		if cluster["name"] == "admin_port_cluster" {
			endpoint := cluster["load_assignment"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)["lb_endpoints"].([]any)[0].(map[string]any)["endpoint"].(map[string]any)
			endpoint["address"].(map[string]any)["socket_address"].(map[string]any)["port_value"] = 9901
		}
		clusters = append(clusters, cluster)
	}
	statics["clusters"] = append(clusters, fixtureStatics["clusters"].([]any)...)
	statics["listeners"] = append(statics["listeners"].([]any), fixtureStatics["listeners"].([]any)...)
	encoded, err := json.Marshal(base)
	return string(encoded), err
}
