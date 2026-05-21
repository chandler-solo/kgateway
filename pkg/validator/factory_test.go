package validator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromEnv_DefaultsToBinary(t *testing.T) {
	t.Setenv(EnvMode, "")
	v := FromEnv()
	_, ok := v.(*binaryValidator)
	assert.True(t, ok, "default mode should be plain binaryValidator")
}

func TestFromEnv_UnknownModeFallsBackToBinary(t *testing.T) {
	t.Setenv(EnvMode, "nonsense")
	v := FromEnv()
	_, ok := v.(*binaryValidator)
	assert.True(t, ok)
}

func TestFromEnv_CacheMode(t *testing.T) {
	t.Setenv(EnvMode, ModeCache)
	v := FromEnv()
	c, ok := v.(*cachingValidator)
	require.True(t, ok, "cache mode should return *cachingValidator")
	_, innerOK := c.inner.(*binaryValidator)
	assert.True(t, innerOK, "cache mode should wrap *binaryValidator")
}

func TestFromEnv_DaemonMode(t *testing.T) {
	t.Setenv(EnvMode, ModeDaemon)
	v := FromEnv()
	c, ok := v.(*cachingValidator)
	require.True(t, ok, "daemon mode should still be wrapped in *cachingValidator")
	d, ok := c.inner.(*daemonValidator)
	require.True(t, ok, "daemon mode should wrap *daemonValidator inside the cache")
	_, innerOK := d.inner.(*binaryValidator)
	assert.True(t, innerOK, "daemon mode should wrap *binaryValidator inside the daemon")
}

func TestEnvInt(t *testing.T) {
	t.Setenv("X_TEST_INT", "")
	assert.Equal(t, 7, envInt("X_TEST_INT", 7))

	t.Setenv("X_TEST_INT", "garbage")
	assert.Equal(t, 7, envInt("X_TEST_INT", 7))

	t.Setenv("X_TEST_INT", "0")
	assert.Equal(t, 7, envInt("X_TEST_INT", 7))

	t.Setenv("X_TEST_INT", "-3")
	assert.Equal(t, 7, envInt("X_TEST_INT", 7))

	t.Setenv("X_TEST_INT", "42")
	assert.Equal(t, 42, envInt("X_TEST_INT", 7))

	t.Setenv("X_TEST_INT", "  123 ")
	assert.Equal(t, 123, envInt("X_TEST_INT", 7))
}

func TestFromEnv_DaemonPoolSizeOverride(t *testing.T) {
	t.Setenv(EnvMode, ModeDaemon)
	t.Setenv(EnvPoolSize, "1")
	v := FromEnv()
	c := v.(*cachingValidator)
	d := c.inner.(*daemonValidator)
	assert.Equal(t, 1, cap(d.sem), "pool size override should be respected")
}
