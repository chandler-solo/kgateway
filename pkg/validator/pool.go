package validator

import (
	"context"
	"runtime"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
)

// DefaultPoolSize is the default worker count for pooledValidator when no
// explicit size is provided. It scales with available CPUs because each worker
// holds an envoy subprocess while validating.
var DefaultPoolSize = runtime.NumCPU()

// pooledValidator wraps an inner Validator with a bounded concurrency pool.
// Callers block on a semaphore slot before invoking the inner validator, so
// at most poolSize envoy subprocesses run in parallel. This caps memory and
// CPU pressure when multiple KRT collections validate concurrently during the
// initial sync, while still letting independent callers proceed in parallel
// up to the limit.
//
// Envoy's `--mode validate` is one-shot (it exits after validating), so the
// pool does not keep envoy processes alive between calls. The win is bounded
// parallelism plus keeping envoy's binary pages hot in the OS page cache.
type pooledValidator struct {
	inner Validator
	sem   chan struct{}
}

// NewPooled wraps v with a bounded concurrency pool of the given size.
// If poolSize <= 0, DefaultPoolSize is used.
func NewPooled(v Validator, poolSize int) Validator {
	if poolSize <= 0 {
		poolSize = DefaultPoolSize
	}
	return &pooledValidator{
		inner: v,
		sem:   make(chan struct{}, poolSize),
	}
}

func (d *pooledValidator) Validate(ctx context.Context, bootstrap *envoybootstrapv3.Bootstrap) error {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.sem }()
	return d.inner.Validate(ctx, bootstrap)
}
