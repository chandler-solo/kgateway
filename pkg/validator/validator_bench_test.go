package validator

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Benchmarks for the validator modes. The underlying envoy invocation is
// stubbed with a fixed-latency fake so the benchmark measures the MODE overhead
// and cache behavior, not envoy itself; the real envoy fork costs 200-500ms per
// call, so measured speedups scale up accordingly. Set KGW_BENCH_REAL_ENVOY=true
// to run the binary-backed variant (requires an envoy binary in PATH; takes
// minutes).

// fakeLatencyValidator stands in for the envoy fork with a fixed cost.
type fakeLatencyValidator struct {
	latency time.Duration
}

func (f *fakeLatencyValidator) Validate(_ context.Context, _ string) error {
	time.Sleep(f.latency)
	return nil
}

// benchLatency models the envoy invocation cost. Kept small so benchmarks
// finish quickly; ratios between modes are what matter.
const benchLatency = 2 * time.Millisecond

func benchConfig(i int) string {
	return fmt.Sprintf(`{"static_resources":{"clusters":[{"name":"cluster-%d","type":"EDS"}]}}`, i)
}

// The duplicate workload models the per-client fan-out: the same config
// content validated over and over (e.g. one backend's cluster across many
// clients and recomputes). CACHE turns repeats into hash lookups.
func BenchmarkValidator_DuplicateWorkload(b *testing.B) {
	config := benchConfig(0)
	for _, tc := range []struct {
		name string
		v    Validator
	}{
		{"binary", &fakeLatencyValidator{latency: benchLatency}},
		{"cache", NewCaching(&fakeLatencyValidator{latency: benchLatency}, 0)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := tc.v.Validate(context.Background(), config); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The unique workload models initial sync over distinct resources: every
// input differs, so the cache cannot help — this pins the worst-case cache
// overhead (a hash + miss per call).
func BenchmarkValidator_UniqueWorkload(b *testing.B) {
	const distinct = 4096
	configs := make([]string, distinct)
	for i := range distinct {
		configs[i] = benchConfig(i)
	}
	for _, tc := range []struct {
		name string
		v    Validator
	}{
		{"binary", &fakeLatencyValidator{latency: benchLatency}},
		{"cache", NewCaching(&fakeLatencyValidator{latency: benchLatency}, distinct)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if err := tc.v.Validate(context.Background(), configs[i%distinct]); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}

// Real envoy variant, opt-in: KGW_BENCH_REAL_ENVOY=true. Measures the actual
// fork cost the cache removes.
func BenchmarkValidator_RealEnvoyDuplicate(b *testing.B) {
	if os.Getenv("KGW_BENCH_REAL_ENVOY") != "true" {
		b.Skip("set KGW_BENCH_REAL_ENVOY=true to benchmark against a real envoy binary")
	}
	config := benchConfig(0)
	for _, tc := range []struct {
		name string
		v    Validator
	}{
		{"binary", NewBinary()},
		{"cache", NewCaching(NewBinary(), 0)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				if err := tc.v.Validate(context.Background(), config); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
