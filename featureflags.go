package posthog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"

	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"
)

const (
	LONG_SCALE                = 0xfffffffffffffff
	bucketingIdentifierDevice = "device_id"
)

var relativeDateRegex = regexp.MustCompile(`^-?([0-9]+)([hdwmy])$`)

// Common sentinel errors for matchProperty — reused to avoid allocating new
// InconclusiveMatchError structs on every evaluation of flags with missing properties.
var (
	errMissingPropertyValue     = &InconclusiveMatchError{"Can't match properties without a given property value"}
	errOperatorIsNotSet         = &InconclusiveMatchError{"Can't match properties with operator is_not_set"}
	errInconclusiveMatch        = &InconclusiveMatchError{"Can't determine if feature flag is enabled or not with given properties"}
	errCohortPropertyValue      = &InconclusiveMatchError{msg: "Can't match cohort without a given cohort property value"}
	errCohortRequiresServerEval = &RequiresServerEvaluationError{msg: "cohort not found in local cohorts - likely a static cohort that requires server evaluation"}
)

// regexCache caches compiled regexps for flag property matching.
// The set of patterns is bounded by the number of flag conditions (loaded once),
// so this cache grows proportionally and avoids re-compiling on every evaluation.
var regexCache sync.Map // map[string]*regexp.Regexp

// getOrCompileRegex returns a cached compiled regexp for the given pattern,
// or compiles and caches it on first use.
func getOrCompileRegex(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp), nil
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	// Store and return — concurrent stores of the same pattern are harmless
	regexCache.Store(pattern, r)
	return r, nil
}

// flagsState holds the feature flag data that is atomically swapped during updates.
// This provides lock-free reads for the common path (flag evaluation).
type flagsState struct {
	featureFlags []FeatureFlag
	flagsByKey   map[string]FeatureFlag // pre-built index for O(1) lookup, avoids rebuilding per evaluation
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
	AggregationGroupTypeIndex *uint8                     `json:"aggregation_group_type_index"`
	Groups                    []FeatureFlagCondition     `json:"groups"`
	Multivariate              *Variants                  `json:"multivariate"`
	Payloads                  map[string]json.RawMessage `json:"payloads"`
	// DecodedPayloads holds pre-decoded string versions of Payloads.
	// Built once at flag load time to avoid per-evaluation json.Unmarshal / unquoting.
	DecodedPayloads map[string]string `json:"-"`
	// VariantLookupTable is pre-computed at flag load time to avoid per-evaluation
	// slice allocation for multivariate flags.
	VariantLookupTable []FlagVariantMeta `json:"-"`
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
	Properties                []FlagProperty `json:"properties"`
	RolloutPercentage         *float64       `json:"rollout_percentage"`
	Variant                   *string        `json:"variant"`
	AggregationGroupTypeIndex *uint8         `json:"aggregation_group_type_index"`
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
	// ParsedValues holds pre-parsed typed values from Values.
	// Built once at flag load time to avoid reconstructing FlagProperty/PropertyGroup
	// from map[string]any on every cohort evaluation.
	ParsedValues []parsedPropertyValue `json:"-"`
}

// parsedPropertyValue is a union holding either a nested PropertyGroup or a FlagProperty.
// Exactly one field is set. Avoids per-evaluation type assertion and reconstruction.
type parsedPropertyValue struct {
	IsGroup  bool
	Group    PropertyGroup
	Property FlagProperty
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
	FeatureFlags        map[string]interface{}     `json:"featureFlags"`
	FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
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

// isServerEvalError returns true if err is a RequiresServerEvaluationError.
// Uses type assertion instead of errors.As to avoid pointer-variable heap escapes.
func isServerEvalError(err error) bool {
	_, ok := err.(*RequiresServerEvaluationError)
	return ok
}

// isInconclusiveError returns true if err is an InconclusiveMatchError.
func isInconclusiveError(err error) bool {
	_, ok := err.(*InconclusiveMatchError)
	return ok
}

// FeatureFlagResult represents the result of a feature flag evaluation,
// containing both the flag value and its payload.
type FeatureFlagResult struct {
	// Key is the feature flag key that was evaluated
	Key string

	// Enabled indicates whether the feature flag evaluation determined
	// the flag to be in an enabled state.
	Enabled bool

	// RawPayload is the serialized JSON payload associated with the flag variant.
	// Nil if no payload is configured.
	// Use GetPayloadAs to unmarshal the payload into a specific type.
	RawPayload *string

	// Variant is the variant key if this is a multivariate flag.
	// Nil for boolean flags.
	Variant *string

	// payloadStore and variantStore hold the actual string values so that
	// RawPayload/Variant can point into the same allocation as the struct itself,
	// avoiding separate heap escapes for the *string pointers.
	payloadStore string
	variantStore string
}

// GetPayloadAs unmarshals the JSON payload into the provided type.
// Returns an error if the payload is empty or cannot be unmarshaled.
func (r *FeatureFlagResult) GetPayloadAs(v interface{}) error {
	if r.RawPayload == nil || *r.RawPayload == "" {
		return errors.New("no payload available")
	}
	return json.Unmarshal([]byte(*r.RawPayload), v)
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
			result, err := poller.matchFeatureFlagProperties(depFlag, distinctId, deviceId, properties, cohorts, flagsByKey, evaluationCache, nil, nil)
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
	localEvaluationEndpoint := "/flags/definitions"
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
				flagsByKey:   currentState.flagsByKey,
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

	// Pre-decode payloads once at load time (avoids per-evaluation json unquoting)
	preDecodePayloads(newFlags)

	// Pre-build flagsByKey index for O(1) lookup during evaluation
	flagsByKey := buildFlagsByKey(newFlags)

	// Store new ETag from response (clear if server stops sending)
	newEtag := res.Header.Get("ETag")

	// Build new groups map
	groups := map[string]string{}
	if featureFlagsResponse.GroupTypeMapping != nil {
		groups = *featureFlagsResponse.GroupTypeMapping
	}

	// Pre-parse cohort values into typed structs (avoids per-evaluation reconstruction)
	parsedCohorts := preParseCohortValues(featureFlagsResponse.Cohorts)

	// Atomic swap of entire state
	poller.state.Store(&flagsState{
		featureFlags: newFlags,
		flagsByKey:   flagsByKey,
		cohorts:      parsedCohorts,
		groups:       groups,
		flagsEtag:    newEtag,
	})
}

func (poller *FeatureFlagsPoller) GetFeatureFlag(flagConfig FeatureFlagPayload) (interface{}, bool, error) {
	flag, err := poller.getFeatureFlag(flagConfig)

	// Make sure person_properties contains distinct_id so /flags request payloads
	// have it under person_properties (matches the batch path and what server expects).
	personProps := mergeDistinctIDIntoProperties(flagConfig.PersonProperties, flagConfig.DistinctId)

	var result interface{}
	locallyEvaluated := false

	if flag.Key != "" {
		result, err = poller.computeFlagLocally(
			flag,
			flagConfig.DistinctId,
			flagConfig.DeviceId,
			flagConfig.Groups,
			personProps,
			flagConfig.GroupProperties,
			poller.getCohorts(),
		)
		locallyEvaluated = err == nil && result != nil
	}

	if err != nil {
		poller.Logger.Warnf("Unable to compute flag locally (%s) - %s", flagConfig.Key, err)
	}

	if (err != nil || result == nil) && !flagConfig.OnlyEvaluateLocally {
		result, err = poller.getFeatureFlagVariant(flagConfig.Key, flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups, personProps, flagConfig.GroupProperties)
		if err != nil {
			return nil, locallyEvaluated, err
		}
	}

	return result, locallyEvaluated, err
}

// mergeDistinctIDIntoProperties returns a copy of properties with distinct_id added
// (if not already present). Used so /flags request payloads always carry distinct_id
// inside person_properties as well as at the top level.
func mergeDistinctIDIntoProperties(properties Properties, distinctID string) Properties {
	if _, ok := properties["distinct_id"]; ok {
		return properties
	}
	merged := make(Properties, len(properties)+1)
	merged["distinct_id"] = distinctID
	for k, v := range properties {
		merged[k] = v
	}
	return merged
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
		if decoded, ok := flag.Filters.DecodedPayloads[variantToString(variant)]; ok {
			return decoded, nil
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

// flagValueAndPayload holds the result of a single flag evaluation that returns
// both the flag value and its payload, avoiding the need for double evaluation.
type flagValueAndPayload struct {
	value            interface{}
	payload          string
	err              error
	locallyEvaluated bool
}

// GetFeatureFlagWithPayload evaluates a feature flag once and returns both its value
// and payload. This avoids the double evaluation that would happen when calling
// GetFeatureFlag and GetFeatureFlagPayload separately.
func (poller *FeatureFlagsPoller) GetFeatureFlagWithPayload(flagConfig FeatureFlagPayload) flagValueAndPayload {
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

	// Try to resolve payload from local evaluation result using pre-decoded payloads
	var payload string
	if err == nil && result != nil {
		variantKey := variantToString(result)
		if decoded, ok := flag.Filters.DecodedPayloads[variantKey]; ok {
			payload = decoded
		}
	}

	locallyEvaluated := err == nil && result != nil

	// Fall back to remote evaluation if local didn't produce a result
	if (err != nil || result == nil) && !flagConfig.OnlyEvaluateLocally {
		flagsResponse, remoteErr := poller.getFeatureFlagVariants(flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups, flagConfig.PersonProperties, flagConfig.GroupProperties)
		if remoteErr != nil {
			return flagValueAndPayload{value: nil, err: remoteErr}
		}
		locallyEvaluated = false
		// Clear local eval error — we successfully made a remote request
		err = nil
		if flagsResponse != nil {
			if flagValue, ok := flagsResponse.FeatureFlags[flagConfig.Key]; ok {
				result = flagValue
			} else {
				// Flag not in remote response — treat as false (matches getFeatureFlagVariant behavior)
				result = false
			}
			if rawPayload, ok := flagsResponse.FeatureFlagPayloads[flagConfig.Key]; ok {
				payload = rawMessageToString(rawPayload)
			}
		} else {
			result = false
		}
	}

	return flagValueAndPayload{value: result, payload: payload, err: err, locallyEvaluated: locallyEvaluated}
}

func (poller *FeatureFlagsPoller) getFeatureFlag(flagConfig FeatureFlagPayload) (FeatureFlag, error) {
	// Wait for initial flag fetch to complete
	<-poller.firstFeatureFlagRequestFinished

	// Use pre-built index for O(1) lookup instead of linear scan
	flagsByKey := poller.getFlagsByKey()
	if flagsByKey == nil {
		return FeatureFlag{}, errors.New("flags were not successfully fetched yet")
	}

	if f, ok := flagsByKey[flagConfig.Key]; ok {
		return f, nil
	}
	return FeatureFlag{}, nil
}

func (poller *FeatureFlagsPoller) GetAllFlags(flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	featureFlags, err := poller.GetFeatureFlags()
	if err != nil {
		return nil, err
	}
	fallbackToDecide := false
	cohorts := poller.getCohorts()

	// Pre-size response map to avoid rehashing as flags are added
	response := make(map[string]interface{}, len(featureFlags))

	// Pre-merge distinct_id into person properties once for the entire batch,
	// instead of copying the map per-flag inside computeFlagLocally.
	personProps := mergeDistinctIDIntoProperties(flagConfig.PersonProperties, flagConfig.DistinctId)

	if len(featureFlags) == 0 {
		fallbackToDecide = true
	} else {
		for _, storedFlag := range featureFlags {
			result, err := poller.computeFlagLocally(
				storedFlag,
				flagConfig.DistinctId,
				flagConfig.DeviceId,
				flagConfig.Groups,
				personProps,
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

	// Use pre-built flagsByKey index (built once when flags are fetched, not per evaluation)
	flagsByKey := poller.getFlagsByKey()

	// evaluationCache is created lazily — only allocated when flag has dependencies.
	// For simple flags (no dependencies), this avoids a map allocation per evaluation.
	var evaluationCache map[string]interface{}
	if flagHasDependencies(flag) {
		evaluationCache = make(map[string]interface{})
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
		return poller.matchFeatureFlagProperties(flag, groups[groupType].(string), nil, focusedGroupProperties, cohorts, flagsByKey, evaluationCache, groups, groupProperties)
	} else {
		localPersonProperties := personProperties
		// Only add distinct_id if the flag has conditions that check person properties.
		// For simple flags (no property conditions), this avoids creating a map that's never read.
		if flagHasPersonProperties(flag) {
			if _, ok := localPersonProperties["distinct_id"]; !ok {
				if personProperties == nil {
					localPersonProperties = Properties{"distinct_id": distinctId}
				} else {
					localPersonProperties = make(Properties, len(personProperties)+1)
					localPersonProperties["distinct_id"] = distinctId
					for k, v := range personProperties {
						localPersonProperties[k] = v
					}
				}
			}
		}
		return poller.matchFeatureFlagProperties(flag, distinctId, deviceId, localPersonProperties, cohorts, flagsByKey, evaluationCache, groups, groupProperties)
	}
}

func getMatchingVariant(flag FeatureFlag, bucketingId string) interface{} {
	// Use pre-computed lookup table if available, otherwise compute on the fly
	lookupTable := flag.Filters.VariantLookupTable
	if lookupTable == nil {
		// Fast path: no multivariate variants means boolean flag — skip hash computation
		if flag.Filters.Multivariate == nil || len(flag.Filters.Multivariate.Variants) == 0 {
			return true
		}
		lookupTable = getVariantLookupTable(flag)
	}
	if len(lookupTable) == 0 {
		return true
	}

	hashValue := calculateHash(flag.Key, bucketingId, "variant")
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
	multivariates := flag.Filters.Multivariate
	if multivariates == nil || multivariates.Variants == nil {
		return nil
	}

	lookupTable := make([]FlagVariantMeta, 0, len(multivariates.Variants))
	valueMin := 0.00
	for _, variant := range multivariates.Variants {
		valueMax := valueMin + *variant.RolloutPercentage/100.
		lookupTable = append(lookupTable, FlagVariantMeta{ValueMin: valueMin, ValueMax: valueMax, Key: variant.Key})
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
	groups Groups,
	groupProperties map[string]Properties,
) (interface{}, error) {
	conditions := flag.Filters.Groups
	bucketingId := getBucketingID(flag, distinctId, deviceId)
	flagAggregation := flag.Filters.AggregationGroupTypeIndex
	groupTypeMapping := poller.getGroups()
	isInconclusive := false

	for _, condition := range conditions {
		// Per-condition aggregation overrides only when the condition explicitly
		// sets its own AggregationGroupTypeIndex that differs from the flag level
		// (mixed targeting). When absent, fall back to the flag-level aggregation
		// so existing pure person and pure group flags keep their original behavior.
		conditionAggregation := condition.AggregationGroupTypeIndex
		if conditionAggregation == nil {
			conditionAggregation = flagAggregation
		}

		effectiveProperties := properties
		effectiveBucketingId := bucketingId

		// Mixed-override path: condition-level aggregation differs from flag-level.
		// This assumes flag-level aggregation is nil for mixed flags.
		if !uint8PtrEqual(conditionAggregation, flagAggregation) {
			if conditionAggregation != nil {
				groupType, exists := groupTypeMapping[fmt.Sprintf("%d", *conditionAggregation)]
				if !exists {
					continue
				}
				groupKey, hasGroup := groups[groupType]
				if !hasGroup {
					continue
				}
				focusedGroupProperties, hasProps := groupProperties[groupType]
				if !hasProps {
					isInconclusive = true
					continue
				}
				groupKeyStr, ok := groupKey.(string)
				if !ok {
					continue
				}
				if _, exists := focusedGroupProperties["$group_key"]; !exists {
					focusedGroupProperties = Properties{"$group_key": groupKey}.Merge(focusedGroupProperties)
				}
				effectiveProperties = focusedGroupProperties
				effectiveBucketingId = groupKeyStr
			}
		}

		isMatch, err := poller.isConditionMatch(flag, distinctId, effectiveBucketingId, deviceId, condition, effectiveProperties, cohorts, flagsByKey, evaluationCache)
		if err != nil {
			// Use direct type switch instead of errors.As to avoid pointer escape allocations.
			// Our error types are returned directly (not wrapped), so type assertion suffices.
			switch err.(type) {
			case *RequiresServerEvaluationError:
				// Static cohort or other missing server-side data - must fallback to API
				return nil, err
			case *InconclusiveMatchError:
				// Evaluation error (bad regex, invalid date, missing property, etc.)
				// Track that we had an inconclusive match, but try other conditions
				isInconclusive = true
			default:
				return nil, err
			}
		}

		if isMatch {
			variantOverride := condition.Variant
			multivariates := flag.Filters.Multivariate

			if variantOverride != nil && multivariates != nil && multivariates.Variants != nil && containsVariant(multivariates.Variants, *variantOverride) {
				return *variantOverride, nil
			} else {
				return getMatchingVariant(flag, effectiveBucketingId), nil
			}
		}
	}

	if isInconclusive {
		return false, errInconclusiveMatch
	}

	return false, nil
}

func uint8PtrEqual(a, b *uint8) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
	cohortId := valueToString(property.Value)
	propertyGroup, ok := cohorts[cohortId]
	if !ok {
		return false, errCohortRequiresServerEval
	}

	return poller.matchPropertyGroup(propertyGroup, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
}

func (poller *FeatureFlagsPoller) matchPropertyGroup(propertyGroup PropertyGroup, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string) (bool, error) {
	groupType := propertyGroup.Type

	// Use pre-parsed values if available (built at load time), otherwise fall back to raw values
	if len(propertyGroup.ParsedValues) > 0 {
		return poller.matchParsedPropertyGroup(groupType, propertyGroup.ParsedValues, properties, cohorts, flagsByKey, evaluationCache, distinctId, deviceId)
	}

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
					if isServerEvalError(err) {
						return false, err
					} else if isInconclusiveError(err) {
						errorMatchingLocally = true
					} else {
						return false, err
					}
				}

				negation := flagProperty.Negation
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
	}

	if errorMatchingLocally {
		return false, errCohortPropertyValue
	}

	return groupType == "AND", nil
}

// matchParsedPropertyGroup evaluates pre-parsed property values without per-evaluation
// reconstruction from map[string]any. This is the fast path for cohort matching.
func (poller *FeatureFlagsPoller) matchParsedPropertyGroup(groupType string, parsedValues []parsedPropertyValue, properties Properties, cohorts map[string]PropertyGroup, flagsByKey map[string]FeatureFlag, evaluationCache map[string]interface{}, distinctId string, deviceId *string) (bool, error) {
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

func matchProperty(property FlagProperty, properties Properties) (bool, error) {
	key := property.Key
	operator := property.Operator
	value := property.Value
	if _, ok := properties[key]; !ok {
		return false, errMissingPropertyValue
	}

	if operator == "is_not_set" {
		return false, errOperatorIsNotSet
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
		return strings.Contains(strings.ToLower(valueToString(override_value)), strings.ToLower(valueToString(value))), nil
	}

	if operator == "not_icontains" {
		return !strings.Contains(strings.ToLower(valueToString(override_value)), strings.ToLower(valueToString(value))), nil
	}

	if operator == "regex" {
		r, err := getOrCompileRegex(valueToString(value))
		// invalid regex
		if err != nil {
			return false, nil
		}

		return r.MatchString(valueToString(override_value)), nil
	}

	if operator == "not_regex" {
		var pattern string

		if valueString, ok := value.(string); ok {
			pattern = valueString
		} else if valueInt, ok := value.(int); ok {
			pattern = strconv.Itoa(valueInt)
		} else {
			return false, errors.New("regex expression not allowed")
		}

		r, err := getOrCompileRegex(pattern)
		// invalid regex
		if err != nil {
			return false, nil
		}

		var match bool
		if valueString, ok := override_value.(string); ok {
			match = r.MatchString(valueString)
		} else if valueInt, ok := override_value.(int); ok {
			match = r.MatchString(strconv.Itoa(valueInt))
		} else {
			return false, errors.New("value type not supported")
		}

		return !match, nil
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

	// Semver comparison operators
	if operator == "semver_eq" || operator == "semver_neq" ||
		operator == "semver_gt" || operator == "semver_gte" ||
		operator == "semver_lt" || operator == "semver_lte" {

		overrideStr, ok := override_value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("semver comparison requires string value, got %T", override_value)}
		}

		overrideParsed, err := parseSemver(overrideStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("property value '%s' is not a valid semver", overrideStr)}
		}

		valueStr, ok := value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value must be a string, got %T", value)}
		}

		valueParsed, err := parseSemver(valueStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value '%s' is not a valid semver", valueStr)}
		}

		cmp := overrideParsed.compareTo(valueParsed)

		switch operator {
		case "semver_eq":
			return cmp == 0, nil
		case "semver_neq":
			return cmp != 0, nil
		case "semver_gt":
			return cmp > 0, nil
		case "semver_gte":
			return cmp >= 0, nil
		case "semver_lt":
			return cmp < 0, nil
		case "semver_lte":
			return cmp <= 0, nil
		}
	}

	if operator == "semver_tilde" {
		overrideStr, ok := override_value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("semver comparison requires string value, got %T", override_value)}
		}

		overrideParsed, err := parseSemver(overrideStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("property value '%s' is not a valid semver", overrideStr)}
		}

		valueStr, ok := value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value must be a string, got %T", value)}
		}

		lower, upper, err := computeTildeBounds(valueStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value '%s' is not valid for tilde operator", valueStr)}
		}

		// Check: lower <= override < upper
		return overrideParsed.compareTo(lower) >= 0 && overrideParsed.compareTo(upper) < 0, nil
	}

	if operator == "semver_caret" {
		overrideStr, ok := override_value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("semver comparison requires string value, got %T", override_value)}
		}

		overrideParsed, err := parseSemver(overrideStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("property value '%s' is not a valid semver", overrideStr)}
		}

		valueStr, ok := value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value must be a string, got %T", value)}
		}

		lower, upper, err := computeCaretBounds(valueStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value '%s' is not valid for caret operator", valueStr)}
		}

		// Check: lower <= override < upper
		return overrideParsed.compareTo(lower) >= 0 && overrideParsed.compareTo(upper) < 0, nil
	}

	if operator == "semver_wildcard" {
		overrideStr, ok := override_value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("semver comparison requires string value, got %T", override_value)}
		}

		overrideParsed, err := parseSemver(overrideStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("property value '%s' is not a valid semver", overrideStr)}
		}

		valueStr, ok := value.(string)
		if !ok {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value must be a string, got %T", value)}
		}

		lower, upper, err := computeWildcardBounds(valueStr)
		if err != nil {
			return false, &InconclusiveMatchError{fmt.Sprintf("flag semver value '%s' is not valid for wildcard operator", valueStr)}
		}

		// Check: lower <= override < upper
		return overrideParsed.compareTo(lower) >= 0 && overrideParsed.compareTo(upper) < 0, nil
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

// semverTuple represents a parsed semantic version as (major, minor, patch).
type semverTuple struct {
	major, minor, patch int
}

// compareTo returns -1 if s < other, 0 if s == other, 1 if s > other.
func (s semverTuple) compareTo(other semverTuple) int {
	if s.major != other.major {
		if s.major < other.major {
			return -1
		}
		return 1
	}
	if s.minor != other.minor {
		if s.minor < other.minor {
			return -1
		}
		return 1
	}
	if s.patch != other.patch {
		if s.patch < other.patch {
			return -1
		}
		return 1
	}
	return 0
}

// parseSemver parses a version string into a semverTuple.
// Parsing rules:
// 1. Strip leading/trailing whitespace
// 2. Strip v or V prefix
// 3. Strip pre-release and build metadata suffixes (split on - or +)
// 4. Split on . and parse first 3 components as integers
// 5. Default missing components to 0
// 6. Ignore extra components beyond the third
// 7. Return error for invalid input
func parseSemver(value string) (semverTuple, error) {
	text := strings.TrimSpace(value)

	// Strip v/V prefix
	if len(text) > 0 && (text[0] == 'v' || text[0] == 'V') {
		text = text[1:]
	}

	if text == "" {
		return semverTuple{}, errors.New("invalid semver: empty string")
	}

	// Find the core version (before any - or +)
	// We need to validate the core before stripping suffixes
	coreEnd := len(text)
	if idx := strings.Index(text, "-"); idx >= 0 {
		coreEnd = idx
	}
	if idx := strings.Index(text, "+"); idx >= 0 && idx < coreEnd {
		coreEnd = idx
	}
	core := text[:coreEnd]

	if core == "" {
		return semverTuple{}, errors.New("invalid semver: empty version before suffix")
	}

	parts := strings.Split(core, ".")
	if len(parts) == 0 || parts[0] == "" {
		return semverTuple{}, errors.New("invalid semver: no version components")
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semverTuple{}, fmt.Errorf("invalid semver: major version '%s' is not a number", parts[0])
	}

	minor := 0
	if len(parts) > 1 {
		if parts[1] == "" {
			return semverTuple{}, errors.New("invalid semver: empty minor version component")
		}
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return semverTuple{}, fmt.Errorf("invalid semver: minor version '%s' is not a number", parts[1])
		}
	}

	patch := 0
	if len(parts) > 2 {
		if parts[2] == "" {
			return semverTuple{}, errors.New("invalid semver: empty patch version component")
		}
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return semverTuple{}, fmt.Errorf("invalid semver: patch version '%s' is not a number", parts[2])
		}
	}

	return semverTuple{major: major, minor: minor, patch: patch}, nil
}

// computeTildeBounds computes the bounds for the tilde (~) operator.
// ~X.Y.Z means >=X.Y.Z and <X.(Y+1).0
func computeTildeBounds(value string) (lower, upper semverTuple, err error) {
	parsed, err := parseSemver(value)
	if err != nil {
		return semverTuple{}, semverTuple{}, err
	}
	lower = parsed
	upper = semverTuple{major: parsed.major, minor: parsed.minor + 1, patch: 0}
	return lower, upper, nil
}

// computeCaretBounds computes the bounds for the caret (^) operator.
// ^X.Y.Z where:
// - X > 0: >=X.Y.Z <(X+1).0.0
// - X == 0, Y > 0: >=0.Y.Z <0.(Y+1).0
// - X == 0, Y == 0: >=0.0.Z <0.0.(Z+1)
func computeCaretBounds(value string) (lower, upper semverTuple, err error) {
	parsed, err := parseSemver(value)
	if err != nil {
		return semverTuple{}, semverTuple{}, err
	}
	lower = parsed

	if parsed.major > 0 {
		upper = semverTuple{major: parsed.major + 1, minor: 0, patch: 0}
	} else if parsed.minor > 0 {
		upper = semverTuple{major: 0, minor: parsed.minor + 1, patch: 0}
	} else {
		upper = semverTuple{major: 0, minor: 0, patch: parsed.patch + 1}
	}
	return lower, upper, nil
}

// computeWildcardBounds computes the bounds for the wildcard (*) operator.
// X.* means >=X.0.0 <(X+1).0.0
// X.Y.* means >=X.Y.0 <X.(Y+1).0
func computeWildcardBounds(value string) (lower, upper semverTuple, err error) {
	text := strings.TrimSpace(value)

	// Strip v/V prefix
	if len(text) > 0 && (text[0] == 'v' || text[0] == 'V') {
		text = text[1:]
	}

	// Remove wildcards and trailing dots
	text = strings.ReplaceAll(text, "*", "")
	text = strings.TrimRight(text, ".")

	if text == "" {
		return semverTuple{}, semverTuple{}, errors.New("invalid wildcard pattern: empty after removing wildcards")
	}

	parts := strings.Split(text, ".")
	// Filter out empty parts
	var nonEmptyParts []string
	for _, p := range parts {
		if p != "" {
			nonEmptyParts = append(nonEmptyParts, p)
		}
	}

	if len(nonEmptyParts) == 0 {
		return semverTuple{}, semverTuple{}, errors.New("invalid wildcard pattern: no version components")
	}

	major, err := strconv.Atoi(nonEmptyParts[0])
	if err != nil {
		return semverTuple{}, semverTuple{}, fmt.Errorf("invalid wildcard pattern: major version '%s' is not a number", nonEmptyParts[0])
	}

	if len(nonEmptyParts) == 1 {
		// X.* pattern
		lower = semverTuple{major: major, minor: 0, patch: 0}
		upper = semverTuple{major: major + 1, minor: 0, patch: 0}
		return lower, upper, nil
	}

	minor, err := strconv.Atoi(nonEmptyParts[1])
	if err != nil {
		return semverTuple{}, semverTuple{}, fmt.Errorf("invalid wildcard pattern: minor version '%s' is not a number", nonEmptyParts[1])
	}

	if len(nonEmptyParts) == 2 {
		// X.Y.* pattern
		lower = semverTuple{major: major, minor: minor, patch: 0}
		upper = semverTuple{major: major, minor: minor + 1, patch: 0}
		return lower, upper, nil
	}

	// X.Y.Z.* pattern - treat as X.Y.Z to X.Y.(Z+1)
	patch, err := strconv.Atoi(nonEmptyParts[2])
	if err != nil {
		return semverTuple{}, semverTuple{}, fmt.Errorf("invalid wildcard pattern: patch version '%s' is not a number", nonEmptyParts[2])
	}
	lower = semverTuple{major: major, minor: minor, patch: patch}
	upper = semverTuple{major: major, minor: minor, patch: patch + 1}
	return lower, upper, nil
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

// flagHasDependencies returns true if any condition in the flag references another flag
// or cohort, requiring an evaluationCache for dependency resolution.
func flagHasDependencies(flag FeatureFlag) bool {
	for _, group := range flag.Filters.Groups {
		for _, prop := range group.Properties {
			if prop.Type == "flag" || prop.Type == "cohort" {
				return true
			}
		}
	}
	return false
}

// flagHasPersonProperties returns true if any condition in the flag checks person properties.
// When false, the distinct_id merge into personProperties can be skipped entirely.
func flagHasPersonProperties(flag FeatureFlag) bool {
	for _, group := range flag.Filters.Groups {
		if len(group.Properties) > 0 {
			return true
		}
	}
	return false
}

// variantToString converts a flag variant value to its string key for payload lookup.
// Flag variants are always bool or string, so we avoid fmt.Sprintf overhead.
// valueToString converts an interface{} value to string without fmt.Sprint allocation
// for common types (string, int, float64). Used for cohort ID lookup.
func valueToString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		return strconv.FormatBool(val)
	default:
		return fmt.Sprint(v)
	}
}

func variantToString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// extracted as a regular func for testing purposes
func checkIfSimpleFlagEnabled(key, bucketingId string, rolloutPercentage float64) bool {
	hash := calculateHash(key, bucketingId, "")
	return hash <= rolloutPercentage/100.
}

// calculateHash computes a deterministic hash value in [0, 1) for flag bucketing.
// The input is: key + "." + distinctId + salt.
// The "." separator is appended internally so callers don't need to concatenate.
func calculateHash(key, distinctId, salt string) float64 {
	// Build the input in a stack-allocated buffer to avoid heap allocations.
	// sha1.Sum takes a complete []byte and returns a [20]byte — no heap escapes.
	totalLen := len(key) + 1 + len(distinctId) + len(salt) // +1 for "."
	var buf [256]byte
	var input []byte
	if totalLen <= len(buf) {
		input = buf[:0]
	} else {
		input = make([]byte, 0, totalLen)
	}
	input = append(input, key...)
	input = append(input, '.')
	input = append(input, distinctId...)
	input = append(input, salt...)

	digest := sha1.Sum(input)
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

// preParseCohortValues converts raw []any values in cohort PropertyGroups into
// typed parsedPropertyValue slices, avoiding per-evaluation reconstruction from map[string]any.
func preParseCohortValues(cohorts map[string]PropertyGroup) map[string]PropertyGroup {
	if cohorts == nil {
		return cohorts
	}
	result := make(map[string]PropertyGroup, len(cohorts))
	for k, pg := range cohorts {
		result[k] = preParsePG(pg)
	}
	return result
}

func preParsePG(pg PropertyGroup) PropertyGroup {
	if len(pg.Values) == 0 {
		return pg
	}
	parsed := make([]parsedPropertyValue, 0, len(pg.Values))
	for _, value := range pg.Values {
		prop, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if _, hasValues := prop["values"]; hasValues {
			// Nested PropertyGroup — recurse
			childPG := PropertyGroup{
				Type:   getSafeProp[string](prop, "type"),
				Values: getSafeProp[[]any](prop, "values"),
			}
			childPG = preParsePG(childPG)
			parsed = append(parsed, parsedPropertyValue{
				IsGroup: true,
				Group:   childPG,
			})
		} else {
			// FlagProperty
			parsed = append(parsed, parsedPropertyValue{
				Property: FlagProperty{
					Key:             getSafeProp[string](prop, "key"),
					Operator:        getSafeProp[string](prop, "operator"),
					Value:           getSafeProp[any](prop, "value"),
					Type:            getSafeProp[string](prop, "type"),
					Negation:        getSafeProp[bool](prop, "negation"),
					DependencyChain: getSafeProp[[]string](prop, "dependency_chain"),
				},
			})
		}
	}
	pg.ParsedValues = parsed
	return pg
}

// preDecodePayloads converts json.RawMessage payloads to strings once at load time,
// so evaluations can use the decoded strings directly without per-call allocation.
func preDecodePayloads(flags []FeatureFlag) {
	for i := range flags {
		if len(flags[i].Filters.Payloads) > 0 {
			decoded := make(map[string]string, len(flags[i].Filters.Payloads))
			for k, raw := range flags[i].Filters.Payloads {
				decoded[k] = rawMessageToString(raw)
			}
			flags[i].Filters.DecodedPayloads = decoded
		}
		// Pre-compute variant lookup table for multivariate flags
		flags[i].Filters.VariantLookupTable = getVariantLookupTable(flags[i])
	}
}

// buildFlagsByKey creates a map from flag key to FeatureFlag for O(1) lookups.
func buildFlagsByKey(flags []FeatureFlag) map[string]FeatureFlag {
	m := make(map[string]FeatureFlag, len(flags))
	for _, f := range flags {
		m[f.Key] = f
	}
	return m
}

// getFlagsByKey returns the pre-built flags-by-key index or nil if not initialized.
func (poller *FeatureFlagsPoller) getFlagsByKey() map[string]FeatureFlag {
	state := poller.state.Load()
	if state == nil {
		return nil
	}
	return state.flagsByKey
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
	return poller.decider.makeFlagsRequest(distinctId, deviceId, groups, personProperties, groupProperties, poller.disableGeoIP, nil)
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
			if isInconclusiveError(err) {
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
		if payload, ok := flagsResponse.FeatureFlagPayloads[key]; ok {
			return rawMessageToString(payload), nil
		}
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
