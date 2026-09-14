package posthog

func (poller *FeatureFlagsPoller) matchCohort(property FlagProperty, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string, groupContext bool, snapshots ...*flagsState) (bool, error) {
	state := poller.evaluationState(snapshots)
	cohortId := valueToString(property.Value)
	propertyGroup, ok := cohorts[cohortId]
	if !ok {
		return false, errCohortRequiresServerEval
	}

	return poller.matchPropertyGroup(propertyGroup, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId, groupContext, state)
}

func (poller *FeatureFlagsPoller) matchPropertyGroup(propertyGroup PropertyGroup, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string, groupContext bool, snapshots ...*flagsState) (bool, error) {
	state := poller.evaluationState(snapshots)
	groupType := propertyGroup.Type

	// Use pre-parsed values if available (built at load time), otherwise fall back to raw values
	if len(propertyGroup.ParsedValues) > 0 {
		return poller.matchParsedPropertyGroup(groupType, propertyGroup.ParsedValues, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId, groupContext, state)
	}

	if len(propertyGroup.Values) == 0 {
		// empty groups are no-ops, always match
		return true, nil
	}

	// Raw values are a compatibility fallback. Convert them to the same typed
	// representation used by production cohorts so evaluation has one code path.
	parsedGroup := preParsePG(propertyGroup)
	return poller.matchParsedPropertyGroup(groupType, parsedGroup.ParsedValues, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId, groupContext, state)
}

// matchParsedPropertyGroup evaluates pre-parsed property values without per-evaluation
// reconstruction from map[string]any. This is the fast path for cohort matching.
func (poller *FeatureFlagsPoller) matchParsedPropertyGroup(groupType string, parsedValues []parsedPropertyValue, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string, groupContext bool, snapshots ...*flagsState) (bool, error) {
	state := poller.evaluationState(snapshots)
	errorMatchingLocally := false

	for i := range parsedValues {
		pv := &parsedValues[i]
		if pv.IsGroup {
			matches, err := poller.matchPropertyGroup(pv.Group, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId, groupContext, state)
			if err != nil {
				if isServerEvalError(err) {
					return false, err
				} else if isInconclusiveError(err) {
					errorMatchingLocally = true
				} else {
					return false, err
				}
			}

			if groupType == "AND" {
				if !matches {
					return false, nil
				}
			} else {
				if matches {
					return true, nil
				}
			}
		} else {
			var matches bool
			var err error
			fp := &pv.Property
			if fp.Type == "cohort" {
				matches, err = poller.matchCohort(*fp, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId, groupContext, state)
			} else if fp.Type == "flag" {
				matches, err = poller.evaluateFlagDependency(*fp, flagsByKey, evaluationCache, distinctId, deviceId, properties, cohorts, groupContext, state)
			} else {
				matches, err = matchProperty(*fp, properties, state.propertyMatchingVersion)
			}

			if err != nil {
				if isServerEvalError(err) {
					return false, err
				} else if isInconclusiveError(err) {
					errorMatchingLocally = true
				} else {
					return false, err
				}
			}

			negation := fp.Negation
			if groupType == "AND" {
				if !matches && !negation {
					return false, nil
				}
				if matches && negation {
					return false, nil
				}
			} else {
				if matches && !negation {
					return true, nil
				}
				if !matches && negation {
					return true, nil
				}
			}
		}
	}

	if errorMatchingLocally {
		return false, errCohortPropertyValue
	}

	return groupType == "AND", nil
}
