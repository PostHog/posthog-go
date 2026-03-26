package posthog

type FeatureFlagPayload struct {
	Key                   string
	DistinctId            string
	DeviceId              *string
	Groups                Groups
	PersonProperties      Properties
	GroupProperties       map[string]Properties
	OnlyEvaluateLocally   bool
	SendFeatureFlagEvents *bool
}

// trueVal is a package-level constant to avoid allocating a new bool pointer on every validate() call.
var trueVal = true

func (c *FeatureFlagPayload) validate() error {
	if len(c.Key) == 0 {
		return ConfigError{
			Reason: "Feature Flag Key required",
			Field:  "Key",
			Value:  c.Key,
		}
	}

	if len(c.DistinctId) == 0 {
		return ConfigError{
			Reason: "DistinctId required",
			Field:  "Distinct Id",
			Value:  c.DistinctId,
		}
	}

	// Groups, PersonProperties, and GroupProperties are intentionally left nil.
	// Nil maps work correctly for local evaluation (nil map reads return zero values).
	// The remote fallback path in makeFlagsRequest handles nil→empty conversion
	// before JSON marshaling.

	if c.SendFeatureFlagEvents == nil {
		c.SendFeatureFlagEvents = &trueVal
	}
	return nil
}

type FeatureFlagPayloadNoKey struct {
	DistinctId            string
	DeviceId              *string
	Groups                Groups
	PersonProperties      Properties
	GroupProperties       map[string]Properties
	OnlyEvaluateLocally   bool
	SendFeatureFlagEvents *bool
}

func (c *FeatureFlagPayloadNoKey) validate() error {
	if len(c.DistinctId) == 0 {
		return ConfigError{
			Reason: "DistinctId required",
			Field:  "Distinct Id",
			Value:  c.DistinctId,
		}
	}

	// Groups, PersonProperties, and GroupProperties are intentionally left nil.
	// Nil maps work correctly for local evaluation (nil map reads return zero values).
	// The remote fallback path in makeFlagsRequest handles nil→empty conversion
	// before JSON marshaling.

	if c.SendFeatureFlagEvents == nil {
		c.SendFeatureFlagEvents = &trueVal
	}
	return nil
}
