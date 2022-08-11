package posthog

type FeatureFlagConfig struct {
	key                   string
	distinctId            string
	defaultResult         bool
	groups                Groups
	personProperties      Properties
	groupProperties       map[string]Properties
	onlyEvaluateLocally   bool
	sendFeatureFlagEvents *bool
}

const DEFAULT_RESULT = false

func (c *FeatureFlagConfig) validate() error {
	if len(c.key) == 0 {
		return ConfigError{
			Reason: "Feature Flag key required",
			Field:  "key",
			Value:  c.key,
		}
	}

	if len(c.distinctId) == 0 {
		return ConfigError{
			Reason: "DistinctId required",
			Field:  "Distinct Id",
			Value:  c.distinctId,
		}
	}

	return nil
}

func makeFeatureFlagConfig(c FeatureFlagConfig) FeatureFlagConfig {

	if c.groups == nil {
		c.groups = Groups{}
	}

	if c.personProperties == nil {
		c.personProperties = NewProperties()
	}

	if c.groupProperties == nil {
		c.groupProperties = map[string]Properties{}
	}

	if c.sendFeatureFlagEvents == nil {
		tempTrue := true
		c.sendFeatureFlagEvents = &tempTrue
	}

	return c
}
