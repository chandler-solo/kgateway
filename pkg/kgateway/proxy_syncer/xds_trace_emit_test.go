package proxy_syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// When XDS_TRACE_OUT names a file, every snapshotPerClient decision made
// while this package's tests run is appended to it as one JSON object per
// line. The formal-methods harness (make formal-lean) sets the variable,
// runs the tests, and replays the recorded trace against the verified xDS
// publication spec with `xdsspec trace`. Without the variable this file is
// inert and the hook stays nil.
func init() {
	path := os.Getenv("XDS_TRACE_OUT")
	if path == "" {
		return
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "XDS_TRACE_OUT: failed to open %s: %v\n", path, err)
		os.Exit(1)
	}
	var mu sync.Mutex
	xdsSnapshotTraceSink = func(event XdsSnapshotTraceEvent) {
		line, err := json.Marshal(event)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = out.Write(append(line, '\n'))
	}
}
