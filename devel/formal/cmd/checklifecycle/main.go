// checklifecycle replays emitted cache/server observations, not Envoy application.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/kgateway-dev/kgateway/v2/devel/formal/lifecycle"
)

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: checklifecycle <trace.jsonl> ...")
	}
	for _, path := range os.Args[1:] {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		events, err := lifecycle.Read(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		stats, err := lifecycle.Check(events)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		data, err := json.Marshal(stats)
		if err != nil {
			return err
		}
		fmt.Printf("PASS %s %s; no application claim\n", path, data)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
