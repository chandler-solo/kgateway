// verify-helm-schema walks values.yaml and values.schema.json in parallel
// and fails if any key present in one is missing from the other. It only
// recurses into a nested object when both sides agree it is an object, so
// user-data keys (e.g. annotation values) and schema-only sub-properties
// under values that default to {} do not produce false positives.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

type schemaNode struct {
	Properties map[string]*schemaNode `json:"properties,omitempty"`
}

func main() {
	valuesPath := flag.String("values", "install/helm/kgateway/values.yaml", "path to values.yaml")
	schemaPath := flag.String("schema", "install/helm/kgateway/values.schema.json", "path to values.schema.json")
	flag.Parse()

	values, err := loadValues(*valuesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *valuesPath, err)
		os.Exit(1)
	}
	schema, err := loadSchema(*schemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *schemaPath, err)
		os.Exit(1)
	}

	missing, extra := compareKeys(values, schema, "")

	ok := true
	if len(missing) > 0 {
		ok = false
		fmt.Println("ERROR: keys in values.yaml but missing from values.schema.json:")
		for _, k := range missing {
			fmt.Printf("  - %s\n", k)
		}
	}
	if len(extra) > 0 {
		ok = false
		fmt.Println("ERROR: keys in values.schema.json but missing from values.yaml:")
		for _, k := range extra {
			fmt.Printf("  - %s\n", k)
		}
	}
	if !ok {
		os.Exit(1)
	}
	fmt.Printf("OK: values.yaml and values.schema.json are in sync (%d top-level keys)\n", len(values))
}

func compareKeys(values map[string]any, schema *schemaNode, prefix string) (missing, extra []string) {
	yamlKeys := keysOf(values)
	var schemaKeys []string
	if schema != nil {
		schemaKeys = make([]string, 0, len(schema.Properties))
		for k := range schema.Properties {
			schemaKeys = append(schemaKeys, k)
		}
	}

	yamlSet := toSet(yamlKeys)
	schemaSet := toSet(schemaKeys)

	missing = sortedDiff(yamlSet, schemaSet, prefix)
	extra = sortedDiff(schemaSet, yamlSet, prefix)

	for _, k := range sortedIntersect(yamlSet, schemaSet) {
		nested, isObj := values[k].(map[string]any)
		if !isObj || len(nested) == 0 {
			continue
		}
		sub := schema.Properties[k]
		if sub == nil || len(sub.Properties) == 0 {
			continue
		}
		m, e := compareKeys(nested, sub, prefix+k+".")
		missing = append(missing, m...)
		extra = append(extra, e...)
	}
	return missing, extra
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toSet(keys []string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s[k] = struct{}{}
	}
	return s
}

func sortedDiff(a, b map[string]struct{}, prefix string) []string {
	out := []string{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, prefix+k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedIntersect(a, b map[string]struct{}) []string {
	out := []string{}
	for k := range a {
		if _, ok := b[k]; ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func loadValues(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func loadSchema(path string) (*schemaNode, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s schemaNode
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
