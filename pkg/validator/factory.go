package validator

import (
	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
)

// New constructs a Validator according to the given settings. The default
// (mode=BINARY) preserves prior behavior. Unknown modes also fall back to
// BINARY so misconfiguration cannot block startup.
func New(s apisettings.Settings) Validator {
	base := NewBinary()
	switch s.ValidatorMode {
	case apisettings.ValidatorCache:
		return NewCaching(base, s.ValidatorCacheSize)
	case apisettings.ValidatorDaemon:
		return NewCaching(NewDaemon(base, s.ValidatorPoolSize), s.ValidatorCacheSize)
	default:
		return base
	}
}
