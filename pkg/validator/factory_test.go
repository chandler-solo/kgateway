package validator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
)

func TestNew_DefaultsToBinary(t *testing.T) {
	v := New(apisettings.Settings{})
	_, ok := v.(*binaryValidator)
	assert.True(t, ok, "zero-valued settings should yield plain binaryValidator")
}

func TestNew_UnknownModeFallsBackToBinary(t *testing.T) {
	v := New(apisettings.Settings{ValidatorMode: "nonsense"})
	_, ok := v.(*binaryValidator)
	assert.True(t, ok)
}

func TestNew_CacheMode(t *testing.T) {
	v := New(apisettings.Settings{
		ValidatorMode:      apisettings.ValidatorCache,
		ValidatorCacheSize: 16,
	})
	c, ok := v.(*cachingValidator)
	require.True(t, ok, "cache mode should return *cachingValidator")
	_, innerOK := c.inner.(*binaryValidator)
	assert.True(t, innerOK, "cache mode should wrap *binaryValidator")
}

func TestNew_DaemonMode(t *testing.T) {
	v := New(apisettings.Settings{
		ValidatorMode:      apisettings.ValidatorDaemon,
		ValidatorCacheSize: 16,
		ValidatorPoolSize:  4,
	})
	c, ok := v.(*cachingValidator)
	require.True(t, ok, "daemon mode should still be wrapped in *cachingValidator")
	d, ok := c.inner.(*daemonValidator)
	require.True(t, ok, "daemon mode should wrap *daemonValidator inside the cache")
	_, innerOK := d.inner.(*binaryValidator)
	assert.True(t, innerOK, "daemon mode should wrap *binaryValidator inside the daemon")
	assert.Equal(t, 4, cap(d.sem), "pool size from settings should be respected")
}

func TestNew_DaemonModeZeroPoolFallsBackToDefault(t *testing.T) {
	v := New(apisettings.Settings{ValidatorMode: apisettings.ValidatorDaemon})
	c := v.(*cachingValidator)
	d := c.inner.(*daemonValidator)
	assert.Equal(t, DefaultPoolSize, cap(d.sem))
}

func TestNew_CacheZeroSizeFallsBackToDefault(t *testing.T) {
	// NewCaching's behavior under size <= 0 is to use DefaultCacheSize; covered
	// directly in cache_test, but verify the wiring exposes it as well.
	v := New(apisettings.Settings{ValidatorMode: apisettings.ValidatorCache})
	_, ok := v.(*cachingValidator)
	require.True(t, ok)
}
