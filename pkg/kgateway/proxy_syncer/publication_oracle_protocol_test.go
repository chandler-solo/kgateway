//go:build formal_oracle

package proxy_syncer

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestPublicationOracleProtocol(t *testing.T) {
	o := startPublicationOracle(t)
	h := newOracleHistory()
	h.send("one", oracleBase())
	valid, _ := json.Marshal(h.request)
	bad := map[string]string{
		"syntax":            "{",
		"schema":            strings.Replace(string(valid), `"schema":1`, `"schema":2`, 1),
		"profile":           strings.Replace(string(valid), "research-publication-v1", "proposed-stack", 1),
		"unknown-field":     strings.Replace(string(valid), `"schema":1`, `"schema":1,"ignored":true`, 1),
		"unknown-event":     strings.Replace(string(valid), `"kind":"build"`, `"kind":"publsih"`, 1),
		"invalid-hash":      strings.Replace(string(valid), `"hash":"1365247732239644193"`, `"hash":"18446744073709551616"`, 1),
		"missing-candidate": `{"schema":1,"profile":"research-publication-v1","events":[{"kind":"publish","client":"one","id":10,"other":0,"input":null}]}`,
	}
	for name, request := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := fmt.Fprintln(o.in, request); err != nil {
				t.Fatal(err)
			}
			line, err := o.out.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var response oracleResponse
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			if response.OK || response.Error == "" {
				t.Fatalf("malformed request accepted: %s", line)
			}
		})
	}
	// Rejection does not poison the next history in the persistent process.
	checkPublicationOracle(t, o, h.request)
	// Regression for nondeterministic map bytes inside nested Any messages.
	i := oracleBase()
	i.Config.Ancillary = []string{"a", "b", "c"}
	_, first, _ := oracleConfigResources(i.Config)
	for range 25 {
		_, next, _ := oracleConfigResources(i.Config)
		if !proto.Equal(first.Items["oracle-listener"].Resource, next.Items["oracle-listener"].Resource) {
			t.Fatal("fixture Any encoding is nondeterministic")
		}
	}
}

func TestPublicationOracleCacheProtocol(t *testing.T) {
	o := startCacheOracle(t)
	r := cacheOracleExamples()["parked-decline"]
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(b)
	bad := map[string]string{
		"syntax":         "{",
		"profile":        strings.Replace(valid, "research-cache-1cd122661-v1", "proposed-stack", 1),
		"unknown-field":  strings.Replace(valid, `"schema":1`, `"schema":1,"ignored":true`, 1),
		"unknown-stream": strings.Replace(valid, `"kind":"open"`, `"kind":"close"`, 1),
		"glob":           strings.Replace(valid, `"names":["a"]`, `"names":["a/*"]`, 1),
	}
	for name, request := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := fmt.Fprintln(o.in, request); err != nil {
				t.Fatal(err)
			}
			line, err := o.out.ReadBytes('\n')
			if err != nil {
				t.Fatal(err)
			}
			var response oracleResponse
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			if response.OK || response.Error == "" {
				t.Fatalf("malformed cache request accepted: %s", line)
			}
		})
	}
	checkCacheOracle(t, o, r)
}
