package validator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubValidator is an inner Validator used to count and program responses.
type stubValidator struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *stubValidator) Validate(_ context.Context, _ *envoybootstrapv3.Bootstrap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *stubValidator) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func bootstrapForNode(id string) *envoybootstrapv3.Bootstrap {
	return &envoybootstrapv3.Bootstrap{
		Node: &envoycorev3.Node{Id: id, Cluster: "c"},
	}
}

func TestCachingValidator_HitsAndMisses(t *testing.T) {
	stub := &stubValidator{}
	v := NewCaching(stub, 16)

	bs := bootstrapForNode("a")
	require.NoError(t, v.Validate(context.Background(), bs))
	require.NoError(t, v.Validate(context.Background(), bs))
	require.NoError(t, v.Validate(context.Background(), bs))
	assert.Equal(t, 1, stub.Calls(), "identical input should hit cache")

	require.NoError(t, v.Validate(context.Background(), bootstrapForNode("b")))
	assert.Equal(t, 2, stub.Calls(), "different input should miss cache")
}

func TestCachingValidator_CachesErrInvalidXDS(t *testing.T) {
	stub := &stubValidator{err: fmt.Errorf("%w: bad cluster cfg", ErrInvalidXDS)}
	v := NewCaching(stub, 16)

	bs := bootstrapForNode("a")
	err1 := v.Validate(context.Background(), bs)
	err2 := v.Validate(context.Background(), bs)
	require.Error(t, err1)
	require.Error(t, err2)
	assert.True(t, errors.Is(err1, ErrInvalidXDS), "first error should chain ErrInvalidXDS")
	assert.True(t, errors.Is(err2, ErrInvalidXDS), "cached error should chain ErrInvalidXDS")
	assert.Equal(t, err1.Error(), err2.Error(), "cached message should match original")
	assert.Equal(t, 1, stub.Calls(), "ErrInvalidXDS should be cached")
}

func TestCachingValidator_DoesNotCacheTransientErrors(t *testing.T) {
	stub := &stubValidator{err: errors.New("envoy validate invocation failed: exec format error")}
	v := NewCaching(stub, 16)

	bs := bootstrapForNode("a")
	for i := 0; i < 3; i++ {
		err := v.Validate(context.Background(), bs)
		require.Error(t, err)
	}
	assert.Equal(t, 3, stub.Calls(), "transient errors must not be cached")
}

func TestCachingValidator_KeyStability(t *testing.T) {
	// Two structurally-identical bootstraps must hash to the same key.
	a := bootstrapForNode("same")
	b := bootstrapForNode("same")
	keyA, _, err := cacheKeyFor(a)
	require.NoError(t, err)
	keyB, _, err := cacheKeyFor(b)
	require.NoError(t, err)
	assert.Equal(t, keyA, keyB)

	// Different content must produce different keys.
	keyC, _, err := cacheKeyFor(bootstrapForNode("different"))
	require.NoError(t, err)
	assert.NotEqual(t, keyA, keyC)
}

func TestCachingValidator_Eviction(t *testing.T) {
	stub := &stubValidator{}
	v := NewCaching(stub, 2) // size 2 to force eviction quickly

	a := bootstrapForNode("a")
	b := bootstrapForNode("b")
	c := bootstrapForNode("c")

	require.NoError(t, v.Validate(context.Background(), a))
	require.NoError(t, v.Validate(context.Background(), b))
	require.NoError(t, v.Validate(context.Background(), c)) // evicts a
	assert.Equal(t, 3, stub.Calls())

	require.NoError(t, v.Validate(context.Background(), a)) // a was evicted
	assert.Equal(t, 4, stub.Calls(), "evicted entry should re-call inner validator")

	require.NoError(t, v.Validate(context.Background(), c)) // c still cached
	assert.Equal(t, 4, stub.Calls(), "still-cached entry should not re-call")
}

func TestCachingValidator_ConcurrentSameKey(t *testing.T) {
	// The cache does not synchronize concurrent misses for the same key, so the
	// inner validator may be called more than once during the race window. That
	// is acceptable: we only need eventual single-call steady state. This test
	// asserts that no panics or wrong results occur under concurrent load.
	stub := &stubValidator{}
	v := NewCaching(stub, 16)
	bs := bootstrapForNode("hot")

	const goroutines = 16
	var wg sync.WaitGroup
	var errs atomic.Int32
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := v.Validate(context.Background(), bs); err != nil {
				errs.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, errs.Load())
	assert.LessOrEqual(t, stub.Calls(), goroutines)
}
