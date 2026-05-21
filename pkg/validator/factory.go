package validator

import (
	"os"
	"strconv"
	"strings"
)

const (
	// EnvMode selects the validator composition. Values: "binary" (default),
	// "cache", "daemon". "cache" wraps binary with an LRU result cache;
	// "daemon" additionally wraps with a bounded concurrency pool.
	EnvMode = "KGW_VALIDATOR_MODE"
	// EnvCacheSize overrides the LRU capacity used by cache and daemon modes.
	EnvCacheSize = "KGW_VALIDATOR_CACHE_SIZE"
	// EnvPoolSize overrides the daemon pool size. Defaults to runtime.NumCPU().
	EnvPoolSize = "KGW_VALIDATOR_POOL_SIZE"

	ModeBinary = "binary"
	ModeCache  = "cache"
	ModeDaemon = "daemon"
)

// FromEnv constructs a Validator according to the KGW_VALIDATOR_* environment
// variables. The default (mode="binary") preserves prior behavior. Unknown
// modes also fall back to binary so misconfiguration cannot block startup.
func FromEnv() Validator {
	base := NewBinary()
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))) {
	case ModeCache:
		return NewCaching(base, envInt(EnvCacheSize, DefaultCacheSize))
	case ModeDaemon:
		return NewCaching(NewDaemon(base, envInt(EnvPoolSize, DefaultPoolSize)), envInt(EnvCacheSize, DefaultCacheSize))
	default:
		return base
	}
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
