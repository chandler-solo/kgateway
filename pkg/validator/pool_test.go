package validator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingValidator returns from Validate only after the test signals it.
// It records the maximum number of concurrent in-flight calls observed.
type blockingValidator struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	release     chan struct{}
	err         error
}

func newBlockingValidator() *blockingValidator {
	return &blockingValidator{release: make(chan struct{})}
}

func (b *blockingValidator) Validate(ctx context.Context, _ *envoybootstrapv3.Bootstrap) error {
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.maxInFlight {
		b.maxInFlight = b.inFlight
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	select {
	case <-b.release:
		return b.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingValidator) releaseAll() {
	close(b.release)
}

func (b *blockingValidator) MaxInFlight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxInFlight
}

func TestPooledValidator_PassThroughSuccess(t *testing.T) {
	inner := &stubValidator{}
	v := NewPooled(inner, 2)
	require.NoError(t, v.Validate(context.Background(), bootstrapForNode("a")))
	assert.Equal(t, 1, inner.Calls())
}

func TestPooledValidator_PassThroughError(t *testing.T) {
	wantErr := errors.New("boom")
	inner := &stubValidator{err: wantErr}
	v := NewPooled(inner, 2)
	err := v.Validate(context.Background(), bootstrapForNode("a"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}

func TestPooledValidator_BoundsConcurrency(t *testing.T) {
	const poolSize = 3
	const callers = 10

	bv := newBlockingValidator()
	v := NewPooled(bv, poolSize)

	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			_ = v.Validate(context.Background(), bootstrapForNode("a"))
		})
	}

	// Wait for concurrency to ramp up to the cap, then release.
	require.Eventually(t, func() bool {
		return bv.MaxInFlight() >= poolSize
	}, 2*time.Second, 5*time.Millisecond)

	bv.releaseAll()
	wg.Wait()

	assert.Equal(t, poolSize, bv.MaxInFlight(), "in-flight calls must not exceed pool size")
}

func TestPooledValidator_PoolExhaustionUnderConcurrentCallers(t *testing.T) {
	// With poolSize=1, ensure that callers serialize through the pool and that
	// the inner validator never sees more than one concurrent call.
	bv := newBlockingValidator()
	v := NewPooled(bv, 1)

	const callers = 8
	var wg sync.WaitGroup
	var done atomic.Int32
	for range callers {
		wg.Go(func() {
			_ = v.Validate(context.Background(), bootstrapForNode("a"))
			done.Add(1)
		})
	}

	// Allow the first caller to acquire the slot.
	require.Eventually(t, func() bool {
		return bv.MaxInFlight() == 1
	}, 2*time.Second, 5*time.Millisecond)

	// No caller should have completed yet (all blocked on release).
	assert.Equal(t, int32(0), done.Load())

	bv.releaseAll()
	wg.Wait()

	assert.Equal(t, int32(callers), done.Load())
	assert.Equal(t, 1, bv.MaxInFlight(), "pool size 1 must serialize callers")
}

func TestPooledValidator_ContextCancellationWhileWaiting(t *testing.T) {
	// One slot is held by a blocked caller; a second caller cancels its context
	// while waiting for the slot and must return ctx.Err() without invoking the
	// inner validator.
	bv := newBlockingValidator()
	v := NewPooled(bv, 1)

	var first sync.WaitGroup
	first.Go(func() {
		_ = v.Validate(context.Background(), bootstrapForNode("a"))
	})
	require.Eventually(t, func() bool {
		return bv.MaxInFlight() == 1
	}, 2*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- v.Validate(ctx, bootstrapForNode("b"))
	}()

	cancel()
	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("validate did not return after context cancellation")
	}

	bv.releaseAll()
	first.Wait()
}

func TestPooledValidator_DefaultPoolSize(t *testing.T) {
	// Zero / negative pool sizes should fall back to DefaultPoolSize rather
	// than dead-lock or panic.
	inner := &stubValidator{}
	v := NewPooled(inner, 0)
	require.NoError(t, v.Validate(context.Background(), bootstrapForNode("a")))

	v = NewPooled(inner, -1)
	require.NoError(t, v.Validate(context.Background(), bootstrapForNode("a")))

	assert.Equal(t, 2, inner.Calls())
}
