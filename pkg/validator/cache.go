package validator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	lru "github.com/hashicorp/golang-lru/v2"
)

// DefaultCacheSize is the default LRU capacity for cachingValidator.
const DefaultCacheSize = 4096

// cachingValidator wraps an inner Validator with an LRU cache keyed on the
// content hash of the marshalled bootstrap. Successful and ErrInvalidXDS
// outcomes are memoized; transient errors (e.g. exec failures) are not, so
// flaky underlying invocations don't get pinned in the cache.
type cachingValidator struct {
	inner Validator
	cache *lru.Cache[string, cachedResult]
}

// cachedResult holds a memoized validation outcome. envoyMsg is the
// normalized envoy error text (empty on success). The wrapping ErrInvalidXDS
// chain is reconstructed on retrieval so callers can still use errors.Is.
type cachedResult struct {
	ok       bool
	envoyMsg string
}

// NewCaching wraps v with an LRU result cache of the given size. If size <= 0,
// DefaultCacheSize is used.
func NewCaching(v Validator, size int) Validator {
	if size <= 0 {
		size = DefaultCacheSize
	}
	cache, err := lru.New[string, cachedResult](size)
	if err != nil {
		// lru.New only errors when size <= 0, which we already guarded against.
		// Fall back to a passthrough validator rather than panicking.
		return v
	}
	return &cachingValidator{inner: v, cache: cache}
}

func (c *cachingValidator) Validate(ctx context.Context, bootstrap *envoybootstrapv3.Bootstrap) error {
	key, _, err := cacheKeyFor(bootstrap)
	if err != nil {
		// If we can't compute a key, fall through to the inner validator.
		return c.inner.Validate(ctx, bootstrap)
	}
	if hit, ok := c.cache.Get(key); ok {
		if hit.ok {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrInvalidXDS, hit.envoyMsg)
	}
	innerErr := c.inner.Validate(ctx, bootstrap)
	if innerErr == nil {
		c.cache.Add(key, cachedResult{ok: true})
		return nil
	}
	if errors.Is(innerErr, ErrInvalidXDS) {
		c.cache.Add(key, cachedResult{envoyMsg: stripErrInvalidXDSPrefix(innerErr.Error())})
	}
	return innerErr
}

// stripErrInvalidXDSPrefix returns the message portion of a wrapped
// ErrInvalidXDS error so it can be re-wrapped consistently on cache retrieval.
func stripErrInvalidXDSPrefix(s string) string {
	prefix := ErrInvalidXDS.Error() + ": "
	return strings.TrimPrefix(s, prefix)
}
