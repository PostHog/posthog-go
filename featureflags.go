package posthog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"

	json "github.com/goccy/go-json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	LONG_SCALE                = 0xfffffffffffffff
	bucketingIdentifierDevice = "device_id"
)

var relativeDateRegex = regexp.MustCompile(`^-?([0-9]+)([hdwmy])$`)

// flagsState holds the feature flag data that is atomically swapped during updates.
// This provides lock-free reads for the common path (flag evaluation).
type flagsState struct {
	featureFlags []FeatureFlag
	cohorts      map[string]PropertyGroup
	groups       map[string]string
	flagsEtag    string
}

type FeatureFlagsPoller struct {
	// firstFeatureFlagRequestFinished is used to log feature flag usage before the first feature flag request is done.
	// After the request the channel get closed.
	firstFeatureFlagRequestFinished chan bool
	shutdown                        chan bool
	forceReload                     chan bool

	// state holds all flag-related data using atomic pointer for lock-free reads
	state atomic.Pointer[flagsState]

	personalApiKey string
	projectApiKey  string
	localEvalUrl   *url.URL
	Logger         Logger
	Endpoint       string
	http           http.Client
	nextPollTick   func() time.Duration
	flagTimeout    time.Duration
	decider        decider
	disableGeoIP   bool
}

type FeatureFlag struct {
	Key                        string   `json:"key"`
	RolloutPercentage          *float64 `json:"rollout_percentage"`
	Active                     bool     `json:"active"`
	Filters                    Filter   `json:"filters"`
	EnsureExperienceContinuity *bool    `json:"ensure_experience_continuity"`
	BucketingIdentifier        *string  `json:"bucketing_identifier"`
}

type Filter struct {
	AggregationGroupTypeIndex *uint8                 `json:"aggregation_group_type_index"`
	Groups                    []FeatureFlagCondition `json:"groups"`
	Multivariate              *Variants              `json:"multivariate"`
	Payloads                  map[string]string      `json:"payloads"`
}

type Variants struct {
	Variants []FlagVariant `json:"variants"`
}

type FlagVariant struct {
	Key               string   `json:"key"`
	Name              string   `json:"name"`
	RolloutPercentage *float64 `json:"rollout_percentage"`
}

type FeatureFlagCondition struct {
	Properties        []FlagProperty `json:"properties"`
	RolloutPercentage *float64       `json:"rollout_percentage"`
	Variant           *string        `json:"variant"`
}

type FlagProperty struct {
	Key             string      `json:"key"`
	Operator        string      `json:"operator"`
	Value           interface{} `json:"value"`
	Type            string      `json:"type"` // Supported types: "person", "group", "cohort", "flag"
	Negation        bool        `json:"negation"`
	DependencyChain []string    `json:"dependency_chain"` // For flag dependencies
}

type PropertyGroup struct {
	Type string `json:"type"`
	// []PropertyGroup or []FlagProperty
	Values []any `json:"values"`
}

type FlagVariantMeta struct {
	ValueMin float64
	ValueMax float64
	Key      string
}

type FeatureFlagsResponse struct {
	Flags            []FeatureFlag            `json:"flags"`
	GroupTypeMapping *map[string]string       `json:"group_type_mapping"`
	Cohorts          map[string]PropertyGroup `json:"cohorts"`
}

type DecideRequestData struct {
	ApiKey           string                `json:"api_key"`
	DistinctId       string                `json:"distinct_id"`
	Groups           Groups                `json:"groups"`
	PersonProperties Properties            `json:"person_properties"`
	GroupProperties  map[string]Properties `json:"group_properties"`
}

type DecideResponse struct {
	FeatureFlags        map[string]interface{} `json:"featureFlags"`
	FeatureFlagPayloads map[string]string      `json:"featureFlagPayloads"`
}

type InconclusiveMatchError struct {
	msg string
}

func (e *InconclusiveMatchError) Error() string {
	return e.msg
}

// RequiresServerEvaluationError is returned when feature flag evaluation
// requires server-side data that is not available locally (e.g., static cohorts,
// experience continuity). This error should propagate immediately to trigger
// API fallback, unlike InconclusiveMatchError which allows trying other conditions.
type RequiresServerEvaluationError struct {
	msg string
}

func (e *RequiresServerEvaluationError) Error() string {
	return e.msg
}

// evaluateFlagDependency evaluates a flag dependency property according to the dependency chain algorithm
func (poller *FeatureFlagsPoller) evaluateFlagDependency(
	property FlagProperty,
	flagsByKey map[string]FeatureFlag,
	evaluationCache map[string]interface{},
	distinctId string,
	deviceId *string,
	properties Properties,
	cohorts map[string]PropertyGroup,
) (bool, error) {
	// Some of these conditions should never happen, but we'll check them to be defensive.
	if property.Value == nil {
		return false, &InconclusiveMatchError{
			msg: fmt.Sprintf("Cannot evaluate flag dependency on '%s' without a value", property.Key),
		}
	}

	if property.Operator != "flag_evaluates_to" {
		return false, &InconclusiveMatchError{
			msg: fmt.Sprintf("Unsupported operator '%s' for flag dependency '%s'", property.Operator, property.Key),
		}
	}

	if flagsByKey == nil || evaluationCache == nil {
		// Cannot evaluate flag dependencies without required context
		return false, &InconclusiveMatchError{
			msg: fmt.Sprintf("Cannot evaluate flag dependency on '%s' without flagsByKey and evaluationCache", property.Key),
		}
	}

	// Check if dependency_chain is present - it should always be provided for flag dependencies
	if property.DependencyChain == nil {
		// Missing dependency_chain indicates malformed server data
		return false, &InconclusiveMatchError{
			msg: fmt.Sprintf("Flag dependency property for '%s' is missing required 'dependency_chain' field", property.Key),
		}
	}

	dependencyChain := property.DependencyChain

	// Handle circular dependency (empty chain means circular)
	if len(dependencyChain) == 0 {
		if poller.Logger != nil {
			poller.Logger.Debugf("Circular dependency detected for flag: %s", property.Key)
		}
		return false, &InconclusiveMatchError{
			msg: fmt.Sprintf("Circular dependency detected for flag '%s'", property.Key),
		}
	}

	// Evaluate all dependencies in the chain order
	for _, depFlagKey := range dependencyChain {
		if _, exists := evaluationCache[depFlagKey]; exists {
			continue
		}

		// Need to evaluate this dependency first
		depFlag, flagExists := flagsByKey[depFlagKey]
		if !flagExists {
			// Missing flag dependency - cannot evaluate locally
			evaluationCache[depFlagKey] = nil
			return false, &InconclusiveMatchError{
				msg: fmt.Sprintf("Cannot evaluate flag dependency '%s' - flag not found in local flags", depFlagKey),
			}
		}

		// Check if the flag is active (same check as in computeFlagLocally)
		if !depFlag.Active {
			evaluationCache[depFlagKey] = false
		} else {
			// Recursively evaluate the dependency
			result, err := poller.matchFeatureFlagProperties(depFlag, distinctId, deviceId, properties, cohorts, flagsByKey, evaluationCache)
			if err != nil {
				// If we can't evaluate a dependency, store nil and propagate the error
				evaluationCache[depFlagKey] = nil
				return false, &InconclusiveMatchError{
					msg: fmt.Sprintf("Cannot evaluate flag dependency '%s': %s", depFlagKey, err.Error()),
				}
			}
			evaluationCache[depFlagKey] = result
		}
	}

	// Check if the dependency result matches the expected value and operator
	if cachedResult, exists := evaluationCache[property.Key]; exists && cachedResult != nil {
		match, err := checkFlagDependencyValue(property.Value, cachedResult)
		if err != nil {
			return false, err
		}
		return match, nil
	}

	// The main dependency couldn't be evaluated
	return false, &InconclusiveMatchError{
		msg: fmt.Sprintf("Flag dependency '%s' could not be evaluated for value comparison", property.Key),
	}
}

// checkFlagDependencyValue checks if a flag dependency result matches the expected value and operator
func checkFlagDependencyValue(expectedValue interface{}, actualResult interface{}) (bool, error) {
	// String variant case - check for exact match or boolean true
	if actualStr, ok := actualResult.(string); ok && len(actualStr) > 0 {
		if expectedBool, ok := expectedValue.(bool); ok {
			// Any variant matches boolean true
			return expectedBool, nil
		} else if expectedStr, ok := expectedValue.(string); ok {
			// variants are case-sensitive, hence our comparison is too
			return actualStr == expectedStr, nil
		} else {
			return false, nil
		}
	}

	// Boolean case - must match expected boolean value
	if actualBool, ok := actualResult.(bool); ok {
		if expectedBool, ok := expectedValue.(bool); ok {
			return actualBool == expectedBool, nil
		}
	}

	// Default case
	return false, nil
}

func newFeatureFlagsPoller(
	projectApiKey string,
	personalApiKey string,
	logger Logger,
	endpoint string,
	httpClient http.Client,
	pollingInterval time.Duration,
	nextPollTick func() time.Duration,
	flagTimeout time.Duration,
	decider decider,
	disableGeoIP bool,
) (*FeatureFlagsPoller, error) {
	localEvaluationEndpoint := "/api/feature_flag/local_evaluation"
	localEvalURL, err := url.Parse(endpoint + localEvaluationEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating local evaluation URL - %w", err)
	}

	if nextPollTick == nil {
		nextPollTick = func() time.Duration { return pollingInterval }
	}

	poller := FeatureFlagsPoller{
		firstFeatureFlagRequestFinished: make(chan bool),
		shutdown:                        make(chan bool),
		forceReload:                     make(chan bool),
		personalApiKey:                  personalApiKey,
		projectApiKey:                   projectApiKey,
		localEvalUrl:                    localEvalURL,
		Logger:                          logger,
		Endpoint:                        endpoint,
		http:                            httpClient,
		nextPollTick:                    nextPollTick,
		flagTimeout:                     flagTimeout,
		decider:                         decider,
		disableGeoIP:                    disableGeoIP,
	}

	go poller.run()
	return &poller, nil
}

func (poller *FeatureFlagsPoller) run() {
	poller.fetchNewFeatureFlags()
	close(poller.firstFeatureFlagRequestFinished)

	for {
		timer := time.NewTimer(poller.nextPollTick())
		select {
		case <-poller.shutdown:
			close(poller.forceReload)
			timer.Stop()
			return
		case <-poller.forceReload:
			timer.Stop()
			poller.fetchNewFeatureFlags()
		case <-timer.C:
			poller.fetchNewFeatureFlags()
		}
	}
}

// fetchNewFeatureFlags fetches the latest feature flag definitions from the PostHog API
// These are used for local evaluation of feature flags and should not be confused with
// the feature flags fetched from the flags API.
func (poller *FeatureFlagsPoller) fetchNewFeatureFlags() {
	personalApiKey := poller.personalApiKey
	headers := http.Header{"Authorization": []string{"Bearer " + personalApiKey}}

	// Read current ETag from state (lock-free)
	currentState := poller.state.Load()
	currentEtag := ""
	if currentState != nil {
		currentEtag = currentState.flagsEtag
	}

	res, cancel, err := poller.localEvaluationFlags(headers, currentEtag)
	if err != nil {
		poller.Logger.Errorf("Unable to fetch feature flags: %s", err)
		return
	}
	defer cancel()
	defer res.Body.Close()

	// Handle 304 Not Modified - flags haven't changed, skip processing
	if res.StatusCode == http.StatusNotModified {
		poller.Logger.Debugf("[FEATURE FLAGS] Flags not modified (304), using cached data")
		// Update ETag if server returned one (preserve existing if not)
		if newEtag := res.Header.Get("ETag"); newEtag != "" && currentState != nil {
			// Atomically swap with updated ETag
			newState := &flagsState{
				featureFlags: currentState.featureFlags,
				cohorts:      currentState.cohorts,
				groups:       currentState.groups,
				flagsEtag:    newEtag,
			}
			poller.state.Store(newState)
		}
		return
	}

	// Handle quota limit response (HTTP 402)
	if res.StatusCode == http.StatusPaymentRequired {
		// Clear existing flags when quota limited - atomic swap
		poller.state.Store(&flagsState{
			featureFlags: []FeatureFlag{},
			cohorts:      map[string]PropertyGroup{},
			groups:       map[string]string{},
			flagsEtag:    "",
		})
		poller.Logger.Warnf("[FEATURE FLAGS] PostHog feature flags quota limited, resetting feature flag data. Learn more about billing limits at https://posthog.com/docs/billing/limits-alerts")
		return
	}

	if res.StatusCode != http.StatusOK {
		poller.Logger.Errorf("Unable to fetch feature flags, status: %s", res.Status)
		return
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		poller.Logger.Errorf("Unable to fetch feature flags: %s", err)
		return
	}
	var featureFlagsResponse FeatureFlagsResponse
	if err = json.Unmarshal(resBody, &featureFlagsResponse); err != nil {
		poller.Logger.Errorf("Unable to unmarshal response from api/feature_flag/local_evaluation: %s", err)
		return
	}
	newFlags := append(make([]FeatureFlag, 0, len(featureFlagsResponse.Flags)), featureFlagsResponse.Flags...)

	// Store new ETag from response (clear if server stops sending)
	newEtag := res.Header.Get("ETag")

	// Build new groups map
	groups := map[string]string{}
	if featureFlagsResponse.GroupTypeMapping != nil {
		groups = *featureFlagsResponse.GroupTypeMapping
	}

	// Atomic swap of entire state
	poller.state.Store(&flagsState{
		featureFlags: newFlags,
		cohorts:      featureFlagsResponse.Cohorts,
		groups:       groups,
		flagsEtag:    newEtag,
	})
}

func (poller *FeatureFlagsPoller) GetFeatureFlag(flagConfig FeatureFlagPayload) (interface{}, error) {
	flag, err := poller.getFeatureFlag(flagConfig)

	var result interface{}

	if flag.Key != "" {
		result, err = poller.computeFlagLocally(
			flag,
			flagConfig.DistinctId,
			flagConfig.DeviceId,
			flagConfig.Groups,
			flagConfig.PersonProperties,
			flagConfig.GroupProperties,
			poller.getCohorts(),
		)
	}

	if err != nil {
		poller.Logger.Warnf("Unable to compute flag locally (%s) - %s", flagConfig.Key, err)
	}

	if (err != nil || result == nil) && !flagConfig.OnlyEvaluateLocally {
		result, err = poller.getFeatureFlagVariant(flagConfig.Key, flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups, flagConfig.PersonProperties, flagConfig.GroupProperties)
		if err != nil {
			return nil, err
		}
	}

	return result, err
}

func (poller *FeatureFlagsPoller) GetFeatureFlagPayload(flagConfig FeatureFlagPayload) (string, error) {
	flag, err := poller.getFeatureFlag(flagConfig)

	var variant interface{}

	if flag.Key != "" {
		variant, err = poller.computeFlagLocally(
			flag,
			flagConfig.DistinctId,
			flagConfig.DeviceId,
			flagConfig.Groups,
			flagConfig.PersonProperties,
			flagConfig.GroupProperties,
			poller.getCohorts(),
		)
	}
	if err != nil {
		poller.Logger.Warnf("Unable to compute flag locally (%s) - %s", flagConfig.Key, err)
	} else if variant != nil {
		payload, ok := flag.Filters.Payloads[fmt.Sprintf("%v", variant)]
		if ok {
			return payload, nil
		}
	}

	if (variant == nil || err != nil) && !flagConfig.OnlyEvaluateLocally {
		result, err := poller.getFeatureFlagPayload(flagConfig.Key, flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups, flagConfig.PersonProperties, flagConfig.GroupProperties)
		if err != nil {
			return "", err
		}

		return result, nil
	}

	return "", errors.New("unable to compute flag locally")
}

func (poller *FeatureFlagsPoller) getFeatureFlag(flagConfig FeatureFlagPayload) (FeatureFlag, error) {
	var featureFlag FeatureFlag
	featureFlags, err := poller.GetFeatureFlags()
	if err != nil {
		return featureFlag, err
	}

	// avoid using flag for conflicts with Golang's stdlib `flag`
	for _, storedFlag := range featureFlags {
		if flagConfig.Key == storedFlag.Key {
			featureFlag = storedFlag
			break
		}
	}

	return featureFlag, nil
}

func (poller *FeatureFlagsPoller) GetAllFlags(flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	response := map[string]interface{}{}
	featureFlags, err := poller.GetFeatureFlags()
	if err != nil {
		return nil, err
	}
	fallbackToDecide := false
	cohorts := poller.getCohorts()

	if len(featureFlags) == 0 {
		fallbackToDecide = true
	} else {
		for _, storedFlag := range featureFlags {
			result, err := poller.computeFlagLocally(
				storedFlag,
				flagConfig.DistinctId,
				flagConfig.DeviceId,
				flagConfig.Groups,
				flagConfig.PersonProperties,
				flagConfig.GroupProperties,
				cohorts,
			)
			if err != nil {
				poller.Logger.Warnf("Unable to compute flag locally (%s) - %s", storedFlag.Key, err)
				fallbackToDecide = true
			} else {
				response[storedFlag.Key] = result
			}
		}
	}

	if fallbackToDecide && !flagConfig.OnlyEvaluateLocally {
		flagsResponse, err := poller.getFeatureFlagVariants(
			flagConfig.DistinctId,
			flagConfig.DeviceId,
			flagConfig.Groups,
			flagConfig.PersonProperties,
			flagConfig.GroupProperties,
		)

		if err != nil {
			return response, err
		}
		if flagsResponse != nil {
			for k, v := range flagsResponse.FeatureFlags {
				response[k] = v
			}
		}
	}

	return response, nil
}
func (poller *FeatureFlagsPoller) computeFlagLocally(
	flag FeatureFlag,
	distinctId string,
	deviceId *string,
	groups Groups,
	personProperties Properties,
	groupProperties map[string]Properties,
	cohorts map[string]PropertyGroup,
) (interface{}, error) {
	if flag.EnsureExperienceContinuity != nil && *flag.EnsureExperienceContinuity {
		return nil, &InconclusiveMatchError{"Flag has experience continuity enabled"}
	}

	if !flag.Active {
		return false, nil
	}

	// Create evaluation cache for flag dependencies
	evaluationCache := make(map[string]interface{})

	// Create flags by key map for dependency evaluation
	featureFlags, err := poller.GetFeatureFlags()
	if err != nil {
		return nil, err
	}
	flagsByKey := make(map[string]FeatureFlag)
	for _, f := range featureFlags {
		flagsByKey[f.Key] = f
	}

	if flag.Filters.AggregationGroupTypeIndex != nil {
		groupType, exists := poller.getGroups()[fmt.Sprintf("%d", *flag.Filters.AggregationGroupTypeIndex)]

		if !exists {
			errMessage := "flag has unknown group type index"
			return nil, errors.New(errMessage)
		}

		groupKey, exists := groups[groupType]

		if !exists {
			errMessage := fmt.Sprintf("[FEATURE FLAGS] Can't compute group feature flag: %s without group names passed in", flag.Key)
			return nil, errors.New(errMessage)
		}

		focusedGroupProperties := groupProperties[groupType]
		if _, ok := focusedGroupProperties["$group_key"]; !ok {
			focusedGroupProperties = Properties{"$group_key": groupKey}.Merge(focusedGroupProperties)
		}
		return poller.matchFeatureFlagProperties(flag, groups[groupType].(string), nil, focusedGroupProperties, cohorts, flagsByKey, evaluationCache)
	} else {
		localPersonProperties := personProperties
		if _, ok := localPersonProperties["distinct_id"]; !ok {
			localPersonProperties = Properties{"distinct_id": distinctId}.Merge(localPersonProperties)
		}
		return poller.matchFeatureFlagProperties(flag, distinctId, deviceId, localPersonProperties, cohorts, flagsByKey, evaluationCache)
	}
}

func getMatchingVariant(flag FeatureFlag, bucketingId string) interface{} {
	lookupTable := getVariantLookupTable(flag)

	hashValue := calculateHash(flag.Key+".", bucketingId, "variant")
	for _, variant := range lookupTable {
		if hashValue >= float64(variant.ValueMin) && hashValue < float64(variant.ValueMax) {
			return variant.Key
		}
	}

	return true
}

func getBucketingID(flag FeatureFlag, distinctId string, deviceId *string) string {
	if flag.BucketingIdentifier != nil && *flag.BucketingIdentifier == bucketingIdentifierDevice && deviceId != nil {
		return *deviceId
	}
	return distinctId
}

func getVariantLookupTable(flag FeatureFlag) []FlagVariantMeta {
	lookupTable := []FlagVariantMeta{}
	valueMin := 0.00

	multivariates := flag.Filters.Multivariate

	if multivariates == nil || multivariates.Variants == nil {
		return lookupTable
	}

	for _, variant := range multivariates.Variants {
		valueMax := valueMin + *variant.RolloutPercentage/100.
		_flagVariantMeta := FlagVariantMeta{ValueMin: valueMin, ValueMax: valueMax, Key: variant.Key}
		lookupTable = append(lookupTable, _flagVariantMeta)
		valueMin = valueMax
	}

	return lookupTable
}

func (poller *FeatureFlagsPoller) matchFeatureFlagProperties(
	flag FeatureFlag,
	distinctId string,
	deviceId *string,
	properties Properties,
	cohorts map[string]PropertyGroup,
	flagsByKey map[string]FeatureFlag,
	evaluationCache map[string]interface{},
) (interface{}, error) {
	conditions := flag.Filters.Groups
	bucketingId := getBucketingID(flag, distinctId, deviceId)
	isInconclusive := false

	for _, condition := range conditions {
		isMatch, err := poller.isConditionMatch(flag, distinctId, bucketingId, deviceId, condition, properties, cohorts, flagsByKey, evaluationCache)
		if err != nil {
			var serverEvalErr *RequiresServerEvaluationError
			if errors.As(err, &serverEvalErr) {
				// Static cohort or other missing server-side data - must fallback to API
				return nil, err
			}

			var inconclusiveErr *InconclusiveMatchError
			if errors.As(err, &inconclusiveErr) {
				// Evaluation error (bad regex, invalid date, missing property, etc.)
				// Track that we had an inconclusive match, but try other conditions
				isInconclusive = true
			} else {
				return nil, err
			}
		}

		if isMatch {
			variantOverride := condition.Variant
			multivariates := flag.Filters.Multivariate

			if variantOverride != nil && multivariates != nil && multivariates.Variants != nil && containsVariant(multivariates.Variants, *variantOverride) {
				return *variantOverride, nil
			} else {
				return getMatchingVariant(flag, bucketingId), nil
			}
		}
	}

	if isInconclusive {
		return false, &InconclusiveMatchError{"Can't determine if feature flag is enabled or not with given properties"}
	}

	return false, nil
}

func (poller *FeatureFlagsPoller) isConditionMatch(
	flag FeatureFlag,
	distinctId string,
	bucketingId string,
	deviceId *string,
	condition FeatureFlagCondition,
	properties Properties,
	cohorts map[string]PropertyGroup,
	flagsByKey map[string]FeatureFlag,
	evaluationCache map[string]interface{},
) (bool, error) {
	if len(condition.Properties) > 0 {
		var (
			isMatch bool
			err     error
		)
		for _, prop := range condition.Properties {
			if prop.Type == "cohort" {
				isMatch, err = poller.matchCohort(prop, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
			} else if prop.Type == "flag" {
				isMatch, err = poller.evaluateFlagDependency(prop, flagsByKey, evaluationCache, distinctId, deviceId, properties, cohorts)
			} else {
				isMatch, err = matchProperty(prop, properties)
			}

			if err != nil || !isMatch {
				return false, err
			}
		}
	}

	if condition.RolloutPercentage != nil {
		return checkIfSimpleFlagEnabled(flag.Key, bucketingId, *condition.RolloutPercentage), nil
	}

	return true, nil
}

func (poller *FeatureFlagsPoller) matchCohort(property FlagProperty, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string) (bool, error) {
	cohortId := fmt.Sprint(property.Value)
	propertyGroup, ok := cohorts[cohortId]
	if !ok {
		return false, &RequiresServerEvaluationError{
			msg: fmt.Sprintf("cohort %s not found in local cohorts - likely a static cohort that requires server evaluation", cohortId),
		}
	}

	return poller.matchPropertyGroup(propertyGroup, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
}

func (poller *FeatureFlagsPoller) matchPropertyGroup(propertyGroup PropertyGroup, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string) (bool, error) {
	groupType := propertyGroup.Type
	values := propertyGroup.Values

	if len(values) == 0 {
		// empty groups are no-ops, always match
		return true, nil
	}

	errorMatchingLocally := false

	for _, value := range values {
		switch prop := value.(type) {
		case map[string]any:
			if _, ok := prop["values"]; ok {
				// PropertyGroup
				matches, err := poller.matchPropertyGroup(PropertyGroup{
					Type:   getSafeProp[string](prop, "type"),
					Values: getSafeProp[[]any](prop, "values"),
				}, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
				if err != nil {
					var serverEvalErr *RequiresServerEvaluationError
					if errors.As(err, &serverEvalErr) {
						// Immediately propagate - this condition requires server-side data
						return false, err
					}

					var inconclusiveErr *InconclusiveMatchError
					if errors.As(err, &inconclusiveErr) {
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
					// OR group
					if matches {
						return true, nil
					}
				}
			} else {
				// FlagProperty
				var matches bool
				var err error
				flagProperty := FlagProperty{
					Key:             getSafeProp[string](prop, "key"),
					Operator:        getSafeProp[string](prop, "operator"),
					Value:           getSafeProp[any](prop, "value"),
					Type:            getSafeProp[string](prop, "type"),
					Negation:        getSafeProp[bool](prop, "negation"),
					DependencyChain: getSafeProp[[]string](prop, "dependency_chain"),
				}
				if prop["type"] == "cohort" {
					matches, err = poller.matchCohort(flagProperty, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
				} else if prop["type"] == "flag" {
					matches, err = poller.evaluateFlagDependency(flagProperty, flagsByKey, evaluationCache, distinctId, deviceId, properties, cohorts)
				} else {
					matches, err = matchProperty(flagProperty, properties)
				}

				if err != nil {
					var serverEvalErr *RequiresServerEvaluationError
					if errors.As(err, &serverEvalErr) {
						// Immediately propagate - this condition requires server-side data
						return false, err
					}

					var inconclusiveErr *InconclusiveMatchError
					if errors.As(err, &inconclusiveErr) {
						errorMatchingLocally = true
					} else {
						return false, err
					}
				}

				negation := flagProperty.Negation
				if groupType == "AND" {
					// if negated property, do the inverse
					if !matches && !negation {
						return false, nil
					}
					if matches && negation {
						return false, nil
					}
				} else {
					// OR group
					if matches && !negation {
						return true, nil
					}
					if !matches && negation {
						return true, nil
					}
				}
			}
		}
	}

	if errorMatchingLocally {
		return false, &InconclusiveMatchError{msg: "Can't match cohort without a given cohort property value"}
	}

	// if we get here, all matched in AND case, or none matched in OR case
	return groupType == "AND", nil
}

func matchProperty(property FlagProperty, properties Properties) (bool, error) {
	key := property.Key
	operator := property.Operator
	value := property.Value
	if _, ok := properties[key]; !ok {
		return false, &InconclusiveMatchError{"Can't match properties without a given property value"}
	}

	if operator == "is_not_set" {
		return false, &InconclusiveMatchError{"Can't match properties with operator is_not_set"}
	}

	override_value := properties[key]

	if operator == "exact" {
		switch t := value.(type) {
		case []interface{}:
			return contains(t, override_value), nil
		default:
			return value == override_value, nil
		}
	}

	if operator == "is_not" {
		switch t := value.(type) {
		case []interface{}:
			return !contains(t, override_value), nil
		default:
			return value != override_value, nil
		}
	}

	if operator == "is_set" {
		return true, nil
	}

	if operator == "icontains" {
		return strings.Contains(strings.ToLower(fmt.Sprintf("%v", override_value)), strings.ToLower(fmt.Sprintf("%v", value))), nil
	}

	if operator == "not_icontains" {
		return !strings.Contains(strings.ToLower(fmt.Sprintf("%v", override_value)), strings.ToLower(fmt.Sprintf("%v", value))), nil
	}

	if operator == "regex" {

		r, err := regexp.Compile(fmt.Sprintf("%v", value))
		// invalid regex
		if err != nil {
			return false, nil
		}

		match := r.MatchString(fmt.Sprintf("%v", override_value))

		if match {
			return true, nil
		} else {
			return false, nil
		}
	}

	if operator == "not_regex" {
		var r *regexp.Regexp
		var err error

		if valueString, ok := value.(string); ok {
			r, err = regexp.Compile(valueString)
		} else if valueInt, ok := value.(int); ok {
			valueString = strconv.Itoa(valueInt)
			r, err = regexp.Compile(valueString)
		} else {
			errMessage := "regex expression not allowed"
			return false, errors.New(errMessage)
		}

		// invalid regex
		if err != nil {
			return false, nil
		}

		var match bool
		if valueString, ok := override_value.(string); ok {
			match = r.MatchString(valueString)
		} else if valueInt, ok := override_value.(int); ok {
			valueString = strconv.Itoa(valueInt)
			match = r.MatchString(valueString)
		} else {
			errMessage := "value type not supported"
			return false, errors.New(errMessage)
		}

		if !match {
			return true, nil
		} else {
			return false, nil
		}
	}

	if operator == "gt" {
		valueOrderable, overrideValueOrderable, err := validateOrderable(value, override_value)
		if err != nil {
			return false, err
		}

		return overrideValueOrderable > valueOrderable, nil
	}

	if operator == "lt" {
		valueOrderable, overrideValueOrderable, err := validateOrderable(value, override_value)
		if err != nil {
			return false, err
		}

		return overrideValueOrderable < valueOrderable, nil
	}

	if operator == "gte" {
		valueOrderable, overrideValueOrderable, err := validateOrderable(value, override_value)
		if err != nil {
			return false, err
		}

		return overrideValueOrderable >= valueOrderable, nil
	}

	if operator == "lte" {
		valueOrderable, overrideValueOrderable, err := validateOrderable(value, override_value)
		if err != nil {
			return false, err
		}

		return overrideValueOrderable <= valueOrderable, nil
	}

	if operator == "is_date_before" || operator == "is_date_after" {
		overrideTime, err := validateDates(override_value)
		if err != nil {
			return false, err
		}

		valueTime, err := validateDates(value)
		if err != nil {
			return false, err
		}

		if operator == "is_date_before" {
			return overrideTime.Before(valueTime), nil
		}

		return overrideTime.After(valueTime), nil
	}

	return false, &InconclusiveMatchError{"Unknown operator: " + operator}

}

func validateOrderable(firstValue interface{}, secondValue interface{}) (float64, float64, error) {
	convertedFirstValue, err := interfaceToFloat(firstValue)
	if err != nil {
		errMessage := "value 1 is not orderable"
		return 0, 0, errors.New(errMessage)
	}
	convertedSecondValue, err := interfaceToFloat(secondValue)
	if err != nil {
		errMessage := "value 2 is not orderable"
		return 0, 0, errors.New(errMessage)
	}

	return convertedFirstValue, convertedSecondValue, nil
}

func validateDates(value interface{}) (time.Time, error) {
	dateStr, ok := value.(string)
	if !ok {
		return time.Time{}, errors.New("date comparison requires string values")
	}

	parsedTime, err := parseDate(dateStr)
	if err != nil {
		return time.Time{}, err
	}

	return parsedTime, nil
}

// parseDate parses a date string which can be in various ISO 8601 formats or relative format (e.g., "-7d")
// Supported formats:
// - RFC3339 with fractional seconds and timezone offsets: "2020-01-01T10:00:00.123Z", "2020-01-01T10:00:00.123+05:30"
// - RFC3339: "2020-01-01T10:00:00Z" or "2020-01-01T10:00:00+00:00"
// - Date only: "2020-01-01"
// - DateTime without timezone: "2020-01-01T10:00:00"
// - Relative: "-7d", "1h", etc.
func parseDate(dateStr string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,      // Handles: fractional seconds, timezone offsets, and Z (e.g., 2024-01-15T10:30:00.123Z, 2024-01-15T10:30:00+05:30)
		time.RFC3339,          // Handles: basic RFC3339 without fractional seconds (e.g., 2024-01-15T10:30:00Z)
		time.DateOnly,         // Handles: date-only format (e.g., 2024-01-15)
		time.DateTime,         // Handles: datetime with space separator (e.g., 2024-01-15 10:30:00)
		"2006-01-02T15:04:05", // Handles: datetime without timezone (e.g., 2024-01-15T10:30:05)
	}

	for _, format := range formats {
		t, err := time.Parse(format, dateStr)
		if err == nil {
			return t, nil
		}
	}

	relativeTime, relErr := parseRelativeDate(dateStr)
	if relErr == nil {
		return relativeTime, nil
	}

	return time.Time{}, errors.New("invalid date format, must be ISO 8601 or relative format (e.g., '-7d')")
}

// parseRelativeDate parses relative date format (e.g., "-1d" or "1d" for 1 day ago).
// Always produces a date in the past.
func parseRelativeDate(dateStr string) (time.Time, error) {
	matches := relativeDateRegex.FindStringSubmatch(dateStr)
	if matches == nil {
		return time.Time{}, errors.New("invalid relative date format")
	}

	number, err := strconv.Atoi(matches[1])
	if err != nil {
		return time.Time{}, errors.New("invalid number in relative date")
	}

	// Avoid overflow or overly large date ranges
	if number >= 10000 {
		return time.Time{}, errors.New("relative date number too large (must be < 10000)")
	}

	interval := matches[2]
	now := time.Now()

	switch interval {
	case "h":
		return now.Add(-time.Duration(number) * time.Hour), nil
	case "d":
		return now.AddDate(0, 0, -number), nil
	case "w":
		return now.AddDate(0, 0, -number*7), nil
	case "m":
		return now.AddDate(0, -number, 0), nil
	case "y":
		return now.AddDate(-number, 0, 0), nil
	default:
		return time.Time{}, errors.New("invalid interval in relative date")
	}
}

func interfaceToFloat(val interface{}) (float64, error) {
	var i float64
	switch t := val.(type) {
	case int:
		i = float64(t)
	case int8:
		i = float64(t)
	case int16:
		i = float64(t)
	case int32:
		i = float64(t)
	case int64:
		i = float64(t)
	case float32:
		i = float64(t)
	case float64:
		i = float64(t)
	case uint8:
		i = float64(t)
	case uint16:
		i = float64(t)
	case uint32:
		i = float64(t)
	case uint64:
		i = float64(t)
	default:
		errMessage := "argument not orderable"
		return 0.0, errors.New(errMessage)
	}

	return i, nil
}

func contains(s []interface{}, e interface{}) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func containsVariant(variantList []FlagVariant, key string) bool {
	for _, variant := range variantList {
		if variant.Key == key {
			return true
		}
	}
	return false
}

// extracted as a regular func for testing purposes
func checkIfSimpleFlagEnabled(key, bucketingId string, rolloutPercentage float64) bool {
	hash := calculateHash(key+".", bucketingId, "")
	return hash <= rolloutPercentage/100.
}

func calculateHash(prefix, distinctId, salt string) float64 {
	hash := sha1.New()
	hash.Write([]byte(prefix + distinctId + salt))
	digest := hash.Sum(nil)
	return float64(binary.BigEndian.Uint64(digest[:8])>>4) / LONG_SCALE
}

func (poller *FeatureFlagsPoller) GetFeatureFlags() ([]FeatureFlag, error) {
	// When channel is open this will block. When channel is closed it will immediately exit.
	<-poller.firstFeatureFlagRequestFinished

	// Lock-free read of state
	state := poller.state.Load()
	if state == nil || state.featureFlags == nil {
		// There was an error with initial flag fetching
		return nil, errors.New("flags were not successfully fetched yet")
	}

	return state.featureFlags, nil
}

// getState returns the current flags state or nil if not initialized.
// This is a lock-free read.
func (poller *FeatureFlagsPoller) getState() *flagsState {
	return poller.state.Load()
}

// getCohorts returns the current cohorts map or an empty map if not initialized.
func (poller *FeatureFlagsPoller) getCohorts() map[string]PropertyGroup {
	state := poller.state.Load()
	if state == nil || state.cohorts == nil {
		return map[string]PropertyGroup{}
	}
	return state.cohorts
}

// getGroups returns the current groups map or an empty map if not initialized.
func (poller *FeatureFlagsPoller) getGroups() map[string]string {
	state := poller.state.Load()
	if state == nil || state.groups == nil {
		return map[string]string{}
	}
	return state.groups
}

func (poller *FeatureFlagsPoller) localEvaluationFlags(headers http.Header, etag string) (*http.Response, context.CancelFunc, error) {
	u := url.URL(*poller.localEvalUrl)
	searchParams := u.Query()
	searchParams.Add("token", poller.projectApiKey)
	searchParams.Add("send_cohorts", "true")
	u.RawQuery = searchParams.Encode()

	if etag != "" {
		headers.Set("If-None-Match", etag)
	}

	return poller.request("GET", u.String(), nil, headers, poller.flagTimeout)
}

func (poller *FeatureFlagsPoller) request(method string, reqUrl string, requestData []byte, headers http.Header, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	req, err := http.NewRequestWithContext(ctx, method, reqUrl, bytes.NewReader(requestData))
	if err != nil {
		poller.Logger.Errorf("creating request - %s", err)
		cancel()
		return nil, nil, err
	}

	version := getVersion()

	req.Header.Add("User-Agent", SDKName+"/"+version)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", fmt.Sprintf("%d", len(requestData)))

	for key, val := range headers {
		req.Header[key] = val
	}

	res, err := poller.http.Do(req)
	if err != nil {
		poller.Logger.Errorf("sending request - %s", err)
	}

	return res, cancel, err
}

func (poller *FeatureFlagsPoller) ForceReload() {
	poller.forceReload <- true
}

func (poller *FeatureFlagsPoller) shutdownPoller() {
	close(poller.shutdown)
}

// getFeatureFlagVariants is a helper function to get the feature flag variants for
// a given distinctId, groups, personProperties, and groupProperties.
// This makes a request to the flags endpoint and returns the response.
// This is used in fallback scenarios where we can't compute the flag locally.
func (poller *FeatureFlagsPoller) getFeatureFlagVariants(distinctId string, deviceId *string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (*FlagsResponse, error) {
	return poller.decider.makeFlagsRequest(distinctId, deviceId, groups, personProperties, groupProperties, poller.disableGeoIP)
}

// getFeatureFlagVariantsLocalOnly evaluates all feature flags using only local evaluation
func (poller *FeatureFlagsPoller) getFeatureFlagVariantsLocalOnly(distinctId string, deviceId *string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (map[string]interface{}, error) {
	flags, err := poller.GetFeatureFlags()
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	cohorts := poller.getCohorts()

	for _, flag := range flags {
			flagValue, err := poller.computeFlagLocally(
				flag,
				distinctId,
				deviceId,
				groups,
				personProperties,
				groupProperties,
				cohorts,
			)

		// Skip flags that can't be evaluated locally (e.g., experience continuity flags)
		if err != nil {
			var inconclusiveErr *InconclusiveMatchError
			if errors.As(err, &inconclusiveErr) {
				continue
			}
			return nil, err
		}

		// Only include flags that are not false
		if flagValue != false {
			result[flag.Key] = flagValue
		}
	}

	return result, nil
}

func (poller *FeatureFlagsPoller) getFeatureFlagVariant(key string, distinctId string, deviceId *string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (interface{}, error) {
	var result interface{} = false

	flagsResponse, variantErr := poller.getFeatureFlagVariants(distinctId, deviceId, groups, personProperties, groupProperties)

	if variantErr != nil {
		return false, variantErr
	}

	for flagKey, flagValue := range flagsResponse.FeatureFlags {
		if key == flagKey {
			return flagValue, nil
		}
	}
	return result, nil
}

func (poller *FeatureFlagsPoller) getFeatureFlagPayload(key string, distinctId string, deviceId *string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (string, error) {
	flagsResponse, err := poller.getFeatureFlagVariants(distinctId, deviceId, groups, personProperties, groupProperties)
	if err != nil {
		return "", err
	}
	if flagsResponse != nil {
		return flagsResponse.FeatureFlagPayloads[key], nil
	}
	return "", nil
}

func getSafeProp[T any](properties map[string]any, key string) T {
	switch v := properties[key].(type) {
	case T:
		return v
	default:
		var defaultValue T
		return defaultValue
	}
}
