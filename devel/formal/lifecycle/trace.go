// Package lifecycle checks observations from controlled cache/server probes.
// It does not infer transport delivery from a callback or application from ACK.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
)

// Event carries metadata only. No secret bodies, protobuf payloads or credentials
// belong in these traces. Stream numbers are probe-local identifiers; cache-api
// profiles use a logical observation scope, not an actual gRPC connection.
type Event struct {
	Schema   int            `json:"schema"`
	Sequence int            `json:"seq"`
	Scenario string         `json:"scenario"`
	Profile  string         `json:"profile"`
	Kind     string         `json:"kind"`
	Key      string         `json:"key,omitempty"`
	Stream   int64          `json:"stream,omitempty"`
	Type     string         `json:"type,omitempty"`
	Version  string         `json:"version,omitempty"`
	Nonce    string         `json:"nonce,omitempty"`
	Request  string         `json:"request,omitempty"`
	Names    []string       `json:"names,omitempty"`
	Status   string         `json:"status,omitempty"`
	Count    int            `json:"count,omitempty"`
	Counts   map[string]int `json:"counts,omitempty"`
}

type Recorder struct {
	mu       sync.Mutex
	scenario string
	profile  string
	events   []Event
}

func New(scenario, profile string) *Recorder {
	return &Recorder{scenario: scenario, profile: profile}
}

func (r *Recorder) Add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Schema, e.Sequence, e.Scenario, e.Profile = 1, len(r.events)+1, r.scenario, r.profile
	e.Names = append([]string(nil), e.Names...)
	r.events = append(r.events, e)
}

// Finish must follow joining all producers. Replay checks reject any missing
// suffix, repeated terminal, or events appended after this terminal.
func (r *Recorder) Finish() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := map[string]int{}
	for _, e := range r.events {
		counts[e.Kind]++
	}
	r.events = append(r.events, Event{
		Schema: 1, Sequence: len(r.events) + 1,
		Scenario: r.scenario, Profile: r.profile, Kind: "end", Count: len(r.events), Counts: counts,
	})
	return append([]Event(nil), r.events...)
}

func Write(w io.Writer, events []Event) error {
	enc := json.NewEncoder(w)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func Read(r io.Reader) ([]Event, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var events []Event
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				return events, nil
			}
			return nil, err
		}
		events = append(events, e)
	}
}

type Stats struct {
	Counts map[string]int `json:"counts"`
	// These are finite observations, not a proof of starvation or eventuality.
	AbsentWatches int `json:"absent_watches"`
	Acknowledged  int `json:"acknowledged_responses"`
}

// Check validates correlation and completeness of EMITTED observations.
// A cache-install event is a successful GetSnapshot readback, possibly after a
// failed SetSnapshot and fan-out; its order relative to callbacks is not causal.
// Instrumentation completeness and arbitrary concurrency remain open.
func Check(events []Event) (Stats, error) {
	s := Stats{Counts: map[string]int{}}
	fail := func(e Event, reason string) (Stats, error) {
		return s, fmt.Errorf("event %d [%s]: %s", e.Sequence, e.Kind, reason)
	}
	if len(events) < 2 {
		return s, errors.New("missing observations and terminal")
	}
	first := events[0]
	if first.Scenario == "" || (first.Profile != "cache-api" && first.Profile != "server-mock") {
		return fail(first, "unknown scenario/profile")
	}
	streams := map[int64]string{}
	seenStreams := map[int64]bool{}
	requests := map[string]Event{}
	responses := map[string]Event{}
	observed := map[string]bool{}
	acknowledgments := map[string]bool{}
	installations := map[string]bool{}
	neededInstallations := map[string]bool{}
	cacheKey := func(e Event) string { return e.Key + "\x00" + e.Type + "\x00" + e.Version }
	responseKey := func(e Event) string { return fmt.Sprintf("%d\x00%s\x00%s", e.Stream, e.Type, e.Nonce) }
	requestKey := func(e Event) string { return fmt.Sprintf("%d\x00%s\x00%s", e.Stream, e.Type, e.Request) }
	for i, e := range events {
		if e.Schema != 1 || e.Sequence != i+1 || e.Scenario != first.Scenario || e.Profile != first.Profile {
			return fail(e, "schema, sequence or profile mismatch")
		}
		if e.Kind == "end" {
			if i != len(events)-1 || e.Count != i || !maps.Equal(e.Counts, s.Counts) {
				return fail(e, "terminal count mismatch or trailing events")
			}
			if len(streams) != 0 || s.Counts["cache-install"] == 0 || s.Counts["request"] == 0 ||
				(s.Counts["watch-status"] == 0 && s.Counts["response-attempt"] == 0) {
				return fail(e, "unfinished stream or vacuous trace")
			}
			for k := range neededInstallations {
				if !installations[k] {
					return fail(e, "response has no matching cache readback")
				}
			}
			return s, nil
		}
		s.Counts[e.Kind]++
		if e.Key == "" {
			return fail(e, "missing cache key")
		}
		if e.Kind == "cache-install" || e.Kind == "publication-result" {
			if e.Type == "" || e.Version == "" || e.Stream != 0 {
				return fail(e, "invalid cache readback")
			}
			if e.Kind == "cache-install" {
				installations[cacheKey(e)] = true
			} else if e.Status != "ok" && e.Status != "context-canceled" {
				return fail(e, "unknown publication result")
			}
			continue
		}
		if e.Stream <= 0 {
			return fail(e, "missing stream/scope")
		}
		if e.Kind == "open" {
			if seenStreams[e.Stream] {
				return fail(e, "reused stream identity")
			}
			streams[e.Stream], seenStreams[e.Stream] = e.Key, true
			continue
		}
		if streams[e.Stream] != e.Key {
			return fail(e, "event outside its open stream/key")
		}
		if e.Kind == "close" {
			delete(streams, e.Stream)
			continue
		}
		if e.Type == "" || e.Request == "" {
			return fail(e, "missing type/request correlation")
		}
		q := requestKey(e)
		if e.Kind == "request" {
			if _, exists := requests[q]; exists {
				return fail(e, "duplicate request identity")
			}
			requests[q] = e
			continue
		}
		request, exists := requests[q]
		if !exists {
			return fail(e, "no matching request")
		}
		switch e.Kind {
		case "watch-status":
			if first.Profile != "cache-api" || (e.Status != "parked" && e.Status != "absent") {
				return fail(e, "unknown watch status/profile")
			}
			if e.Status == "absent" {
				s.AbsentWatches++
			}
		case "cache-response":
			if first.Profile != "cache-api" || e.Version == "" {
				return fail(e, "invalid cache response/profile")
			}
			neededInstallations[cacheKey(e)] = true
		case "response-attempt":
			k := responseKey(e)
			if first.Profile != "server-mock" || e.Nonce == "" || e.Version == "" {
				return fail(e, "invalid response callback/profile")
			}
			if _, duplicate := responses[k]; duplicate {
				return fail(e, "duplicate response nonce")
			}
			responses[k] = e
			neededInstallations[cacheKey(e)] = true
		case "transport-observed":
			k := responseKey(e)
			r, ok := responses[k]
			if !ok || observed[k] || r.Version != e.Version || r.Request != e.Request {
				return fail(e, "transport observation lacks matching response callback")
			}
			observed[k] = true
		case "ack", "nack":
			k := responseKey(e)
			r, ok := responses[k]
			if !ok || !observed[k] || request.Nonce != e.Nonce || request.Version != e.Version {
				return fail(e, "unmatched acknowledgment")
			}
			if e.Kind == "ack" && r.Version != e.Version {
				return fail(e, "ACK version mismatch")
			}
			if acknowledgments[q] {
				return fail(e, "duplicate acknowledgment observation for request")
			}
			acknowledgments[q] = true
			if e.Kind == "ack" {
				s.Acknowledged++
			}
		default:
			return fail(e, "unknown event (application is outside this trace profile)")
		}
	}
	return s, errors.New("missing terminal")
}
