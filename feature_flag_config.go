package posthog

// FeatureFlagPayload configures a single legacy feature flag evaluation.
// It is used by Client.GetFeatureFlag, Client.IsFeatureEnabled,
// Client.GetFeatureFlagResult, and Client.GetFeatureFlagPayload.
type FeatureFlagPayload struct {
	// Key is the feature flag key to evaluate. It is required.
	Key string
	// DistinctId is the user distinct ID to evaluate the flag for. It is required.
	DistinctId string
	// DeviceId optionally provides a device_id for remote /flags requests and event deduplication.
	DeviceId *string
	// Groups supplies group identifiers for group-targeted flags.
	Groups Groups
	// PersonProperties overrides person properties used during flag evaluation.
	PersonProperties Properties
	// GroupProperties overrides group properties used during flag evaluation, keyed by group type.
	GroupProperties map[string]Properties
	// OnlyEvaluateLocally prevents fallback to remote /flags requests.
	OnlyEvaluateLocally bool
	// SendFeatureFlagEvents controls whether $feature_flag_called is captured.
	// Nil defaults to true during validation.
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

	return validateFeatureFlagBase(c.DistinctId, &c.SendFeatureFlagEvents)
}

func validateFeatureFlagBase(distinctId string, sendFeatureFlagEvents **bool) error {
	if len(distinctId) == 0 {
		return ConfigError{
			Reason: "DistinctId required",
			Field:  "Distinct Id",
			Value:  distinctId,
		}
	}

	// Groups, PersonProperties, and GroupProperties are intentionally left nil.
	// Nil maps work correctly for local evaluation (nil map reads return zero values).
	// The remote fallback path in makeFlagsRequest handles nil→empty conversion
	// before JSON marshaling.

	if *sendFeatureFlagEvents == nil {
		*sendFeatureFlagEvents = &trueVal
	}
	return nil
}

// FeatureFlagPayloadNoKey configures legacy evaluation of all flags for one user.
// It is used by Client.GetAllFlags.
type FeatureFlagPayloadNoKey struct {
	// DistinctId is the user distinct ID to evaluate flags for. It is required.
	DistinctId string
	// DeviceId optionally provides a device_id for remote /flags requests and event deduplication.
	DeviceId *string
	// Groups supplies group identifiers for group-targeted flags.
	Groups Groups
	// PersonProperties overrides person properties used during flag evaluation.
	PersonProperties Properties
	// GroupProperties overrides group properties used during flag evaluation, keyed by group type.
	GroupProperties map[string]Properties
	// OnlyEvaluateLocally prevents fallback to remote /flags requests.
	OnlyEvaluateLocally bool
	// SendFeatureFlagEvents is reserved for parity with single-flag payloads.
	// Nil defaults to true during validation; GetAllFlags does not currently emit per-flag access events.
	SendFeatureFlagEvents *bool
}

func (c *FeatureFlagPayloadNoKey) validate() error {
	return validateFeatureFlagBase(c.DistinctId, &c.SendFeatureFlagEvents)
}
