package lifecycle

import (
	"bytes"
	"strings"
	"testing"
)

func acknowledgedTrace() []Event {
	r := New("ack-without-application", "server-mock")
	r.Add(Event{Kind: "cache-install", Key: "k", Type: "EDS", Version: "v1"})
	r.Add(Event{Kind: "open", Key: "k", Stream: 1})
	r.Add(Event{Kind: "request", Key: "k", Stream: 1, Type: "EDS", Request: "r1"})
	r.Add(Event{Kind: "response-attempt", Key: "k", Stream: 1, Type: "EDS", Request: "r1", Version: "v1", Nonce: "n1"})
	r.Add(Event{Kind: "transport-observed", Key: "k", Stream: 1, Type: "EDS", Request: "r1", Version: "v1", Nonce: "n1"})
	r.Add(Event{Kind: "request", Key: "k", Stream: 1, Type: "EDS", Request: "r2", Version: "v1", Nonce: "n1"})
	r.Add(Event{Kind: "ack", Key: "k", Stream: 1, Type: "EDS", Request: "r2", Version: "v1", Nonce: "n1"})
	r.Add(Event{Kind: "close", Key: "k", Stream: 1})
	return r.Finish()
}

func TestAcknowledgmentDoesNotClaimApplication(t *testing.T) {
	events := acknowledgedTrace()
	stats, err := Check(events)
	if err != nil || stats.Acknowledged != 1 || stats.Counts["application"] != 0 {
		t.Fatalf("stats=%+v error=%v", stats, err)
	}
	var buf bytes.Buffer
	if err := Write(&buf, events); err != nil {
		t.Fatal(err)
	}
	decoded, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Check(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestRejectIncompleteOrUncorrelatedLifecycle(t *testing.T) {
	mutations := map[string]func([]Event) []Event{
		"empty":                func(_ []Event) []Event { return nil },
		"truncated":            func(e []Event) []Event { return e[:len(e)-1] },
		"duplicate-terminal":   func(e []Event) []Event { return append(e, e[len(e)-1]) },
		"lost-event":           func(e []Event) []Event { return append(e[:3], e[4:]...) },
		"wrong-schema":         func(e []Event) []Event { e[2].Schema = 2; return e },
		"mixed-profile":        func(e []Event) []Event { e[2].Profile = "cache-api"; return e },
		"wrong-count":          func(e []Event) []Event { e[len(e)-1].Counts["ack"] = 2; return e },
		"unknown-event":        func(e []Event) []Event { e[3].Kind = "send"; return e },
		"ack-as-application":   func(e []Event) []Event { e[6].Kind = "application"; return e },
		"wrong-nonce":          func(e []Event) []Event { e[6].Nonce = "other"; return e },
		"wrong-ack-version":    func(e []Event) []Event { e[5].Version = "v2"; e[6].Version = "v2"; return e },
		"wrong-stream":         func(e []Event) []Event { e[4].Stream = 2; return e },
		"wrong-key":            func(e []Event) []Event { e[4].Key = "other"; return e },
		"wrong-request":        func(e []Event) []Event { e[4].Request = "other"; return e },
		"unobserved-version":   func(e []Event) []Event { e[0].Version = "other"; return e },
		"unobserved-transport": func(e []Event) []Event { e[4].Kind = "watch-status"; e[4].Status = "parked"; return e },
		"reused-stream":        func(e []Event) []Event { e[7].Kind = "open"; return e },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := Check(mutate(acknowledgedTrace())); err == nil {
				t.Fatal("invalid lifecycle accepted")
			}
		})
	}
}

func TestRejectUnknownJSONField(t *testing.T) {
	if _, err := Read(strings.NewReader(`{"schema":1,"guessed_application":true}`)); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
}
