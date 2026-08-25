package posthog

// matchParsedPropertyGroup evaluates pre-parsed property values without per-evaluation
// reconstruction from map[string]any. This is the fast path for cohort matching.
func matchParsedPropertyGroup(poller *FeatureFlagsPoller, groupType string, parsedValues []parsedPropertyValue, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string) (bool, error) {
	errorMatchingLocally := false

	for i := range parsedValues {
		pv := &parsedValues[i]
		if pv.IsGroup {
			matches, err := poller.matchPropertyGroup(pv.Group, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
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
				matches, err = poller.matchCohort(*fp, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
			} else if fp.Type == "flag" {
				matches, err = poller.evaluateFlagDependency(*fp, flagsByKey, evaluationCache, distinctId, deviceId, properties, cohorts)
			} else {
				matches, err = matchProperty(*fp, properties)
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
