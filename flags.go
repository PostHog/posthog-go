package posthog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	json "github.com/goccy/go-json"
)

// FlagsRequestData is the wire-format request body sent to the /flags endpoint.
type FlagsRequestData struct {
	// ApiKey is the PostHog project API key.
	ApiKey string `json:"api_key"`
	// DistinctId is the user distinct ID to evaluate flags for.
	DistinctId string `json:"distinct_id"`
	// DeviceId optionally identifies the device for device-aware flag evaluation.
	DeviceId *string `json:"device_id,omitempty"`
	// Groups contains group identifiers for group-targeted flags.
	Groups Groups `json:"groups"`
	// PersonProperties overrides person properties for this evaluation.
	PersonProperties Properties `json:"person_properties"`
	// GroupProperties overrides group properties for this evaluation, keyed by group type.
	GroupProperties map[string]Properties `json:"group_properties"`
	// DisableGeoIP disables GeoIP enrichment for this flags request.
	DisableGeoIP bool `json:"geoip_disable,omitempty"`
	// FlagKeysToEvaluate asks the server to evaluate only these flag keys when non-empty.
	FlagKeysToEvaluate []string `json:"flag_keys_to_evaluate,omitempty"`
}

// FlagDetail represents one evaluated feature flag in the v4 /flags response format.
type FlagDetail struct {
	// Key is the feature flag key.
	Key string `json:"key"`
	// Enabled reports whether the flag is enabled for the evaluated user.
	Enabled bool `json:"enabled"`
	// Variant is the matched variant key for multivariate flags, or nil for boolean flags.
	Variant *string `json:"variant"`
	// Reason explains why the flag matched or did not match, when provided by the API.
	Reason *FlagReason `json:"reason"`
	// Metadata contains IDs, version, payload, and optional flag description.
	Metadata FlagMetadata `json:"metadata"`
	// Failed is true when the API could not evaluate this flag due to a transient failure.
	Failed *bool `json:"failed,omitempty"`
}

// FlagReason represents why a flag was enabled or disabled.
type FlagReason struct {
	// Code is the machine-readable reason code returned by the API.
	Code string `json:"code"`
	// Description is the human-readable reason returned by the API.
	Description string `json:"description"`
	// ConditionIndex is the matched condition index, when available.
	ConditionIndex *int `json:"condition_index"`
}

// FlagMetadata contains additional information about a flag evaluation.
type FlagMetadata struct {
	// ID is the numeric feature flag ID in PostHog.
	ID int `json:"id"`
	// Version is the feature flag version evaluated by the API.
	Version int `json:"version"`
	// Payload is the raw JSON payload associated with the matched flag value.
	Payload json.RawMessage `json:"payload"`
	// Description is the feature flag description, when returned by the API.
	Description *string `json:"description,omitempty"`
	// HasExperiment reports whether the flag is linked to an experiment.
	// Nil when the server does not send it (older deployments).
	HasExperiment *bool `json:"has_experiment"`
}

// GetValue returns the variant string when Variant is set; otherwise it returns Enabled.
func (f FlagDetail) GetValue() interface{} {
	if f.Variant != nil {
		return *f.Variant
	}
	return f.Enabled
}

// NewFlagDetail creates a FlagDetail from a flag key, a bool or string value,
// and an optional raw JSON payload. String values are treated as enabled variants;
// bool values are treated as boolean flag results.
func NewFlagDetail(key string, value interface{}, payload json.RawMessage) FlagDetail {
	var variant *string
	var enabled bool

	switch v := value.(type) {
	case string:
		variant = &v
		enabled = true
	case bool:
		enabled = v
	default:
		enabled = false
	}

	return FlagDetail{
		Key:     key,
		Enabled: enabled,
		Variant: variant,
		Reason:  nil,
		Metadata: FlagMetadata{
			Payload: payload,
		},
	}
}

// FlagsResponse represents a normalized /flags response.
// It accepts both legacy v3 and current v4 response shapes.
type FlagsResponse struct {
	// CommonResponseFields contains response metadata shared across formats.
	CommonResponseFields

	// Flags contains v4-format flag details keyed by flag key.
	Flags map[string]FlagDetail `json:"flags,omitempty"`

	// FeatureFlags contains legacy v3 flag values keyed by flag key.
	FeatureFlags map[string]interface{} `json:"featureFlags"`
	// FeatureFlagPayloads contains legacy v3 raw payloads keyed by flag key.
	FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
}

// CommonResponseFields contains fields common to all flags response versions.
type CommonResponseFields struct {
	// QuotaLimited lists quota-limited resources returned by the API.
	QuotaLimited []string `json:"quota_limited"`
	// RequestId is the API request identifier for tracing flag evaluation.
	RequestId string `json:"requestId"`
	// EvaluatedAt is the server evaluation timestamp, when returned.
	EvaluatedAt *int64 `json:"evaluatedAt"`
	// ErrorsWhileComputingFlags reports whether the server had errors computing any flags.
	ErrorsWhileComputingFlags bool `json:"errorsWhileComputingFlags"`
}

// UnmarshalJSON implements custom unmarshaling to handle both v3 and v4 formats
func (r *FlagsResponse) UnmarshalJSON(data []byte) error {
	// First try v4 format
	type V4Response struct {
		CommonResponseFields
		Flags map[string]FlagDetail `json:"flags"`
	}

	var v4 V4Response
	if err := json.Unmarshal(data, &v4); err == nil && v4.Flags != nil {
		// It's a v4 response
		r.Flags = v4.Flags
		r.CommonResponseFields = v4.CommonResponseFields

		// Calculate v3 format fields from Flags
		r.FeatureFlags = make(map[string]interface{})
		r.FeatureFlagPayloads = make(map[string]json.RawMessage)
		for key, flag := range r.Flags {
			r.FeatureFlags[key] = flag.GetValue()
			r.FeatureFlagPayloads[key] = flag.Metadata.Payload
		}
		return nil
	}

	// If not v4, try v3 format
	type V3Response struct {
		CommonResponseFields
		FeatureFlags        map[string]interface{}     `json:"featureFlags"`
		FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
	}

	var v3 V3Response
	if err := json.Unmarshal(data, &v3); err != nil {
		return err
	}

	// Store v3 format data
	r.FeatureFlags = v3.FeatureFlags
	r.FeatureFlagPayloads = v3.FeatureFlagPayloads
	r.CommonResponseFields = v3.CommonResponseFields

	// Construct v4 format from v3 data
	r.Flags = make(map[string]FlagDetail)
	for key, value := range v3.FeatureFlags {
		var payloadRaw json.RawMessage
		if payload, ok := v3.FeatureFlagPayloads[key]; ok {
			payloadRaw = payload
		}
		r.Flags[key] = NewFlagDetail(key, value, payloadRaw)
	}
	return nil
}

// decider defines the interface for making flags requests
type decider interface {
	makeFlagsRequest(distinctId string, deviceId *string, groups Groups, personProperties Properties,
		groupProperties map[string]Properties, disableGeoIP bool, flagKeys []string) (*FlagsResponse, error)
}

// flagsClient implements the decider interface
type flagsClient struct {
	apiKey                    string
	endpoint                  string
	http                      http.Client
	featureFlagRequestTimeout time.Duration
	logger                    Logger
	retryAfter                func(int) time.Duration
	maxAttempts               int
}

const defaultFeatureFlagRequestMaxRetries = 1
const defaultFlagsRequestMaxAttempts = 1 + defaultFeatureFlagRequestMaxRetries

func defaultFlagsBackoff() *Backoff {
	return NewBackoff(300*time.Millisecond, 2, 0, 30*time.Second)
}

// newFlagsClient creates a new flagsClient
func newFlagsClient(apiKey string, endpoint string, httpClient http.Client,
	featureFlagRequestTimeout time.Duration, logger Logger, maxRetries *int) (*flagsClient, error) {

	// Try v2 endpoint first
	flagsEndpoint := "flags/?v=2"
	flagsEndpointURL, err := url.Parse(endpoint + "/" + flagsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating url: %v", err)
	}

	maxAttempts := defaultFlagsRequestMaxAttempts
	if maxRetries != nil {
		maxAttempts = 1 + *maxRetries
	}

	return &flagsClient{
		apiKey:                    apiKey,
		endpoint:                  flagsEndpointURL.String(),
		http:                      httpClient,
		featureFlagRequestTimeout: featureFlagRequestTimeout,
		logger:                    logger,
		retryAfter:                defaultFlagsBackoff().Duration,
		maxAttempts:               maxAttempts,
	}, nil
}

// makeFlagsRequest makes a request to the flags endpoint and deserializes the response
// into a FlagsResponse struct.
func (d *flagsClient) makeFlagsRequest(distinctId string, deviceId *string, groups Groups, personProperties Properties,
	groupProperties map[string]Properties, disableGeoIP bool, flagKeys []string) (*FlagsResponse, error) {
	// Ensure non-nil maps for JSON marshaling (nil marshals as "null", server expects "{}")
	if groups == nil {
		groups = Groups{}
	}
	if personProperties == nil {
		personProperties = Properties{}
	}
	if groupProperties == nil {
		groupProperties = map[string]Properties{}
	}
	requestData := FlagsRequestData{
		ApiKey:             d.apiKey,
		DistinctId:         distinctId,
		DeviceId:           deviceId,
		Groups:             groups,
		PersonProperties:   personProperties,
		GroupProperties:    groupProperties,
		DisableGeoIP:       disableGeoIP,
		FlagKeysToEvaluate: flagKeys,
	}

	requestDataBytes, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal flags endpoint request data: %v", err)
	}

	var resBody []byte
	attempts := d.maxAttempts
	for attempt := 0; attempt < attempts; attempt++ {
		// Create a fresh request and body for each attempt.
		ctx, cancel := context.WithTimeout(context.Background(), d.featureFlagRequestTimeout)
		req, err := http.NewRequestWithContext(ctx, "POST", d.endpoint, bytes.NewReader(requestDataBytes))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("creating request: %v", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "posthog-go/"+Version)

		res, err := d.http.Do(req)
		if err != nil {
			cancel()
			requestErr := fmt.Errorf("sending request: %w", err)
			if attempt == attempts-1 || !isRetryableFlagsRequestError(err) {
				return nil, requestErr
			}
			d.sleepBeforeFlagsRetry(attempt)
			continue
		}

		if res.StatusCode != http.StatusOK {
			statusCode := res.StatusCode
			res.Body.Close()
			cancel()
			if attempt != attempts-1 && isRetryableFlagsStatusCode(statusCode) {
				d.sleepBeforeFlagsRetry(attempt)
				continue
			}
			return nil, NewAPIError(statusCode, fmt.Sprintf("unexpected status code from /flags/: %d", statusCode))
		}

		resBody, err = io.ReadAll(res.Body)
		res.Body.Close()
		cancel()
		if err != nil {
			readErr := fmt.Errorf("error reading response from /flags/: %w", err)
			if attempt == attempts-1 || !isRetryableFlagsRequestError(err) {
				return nil, readErr
			}
			d.sleepBeforeFlagsRetry(attempt)
			continue
		}
		break
	}

	var flagsResponse FlagsResponse
	err = json.Unmarshal(resBody, &flagsResponse)
	if err != nil {
		return nil, fmt.Errorf("error parsing response from /flags/: %v", err)
	}

	if flagsResponse.ErrorsWhileComputingFlags {
		d.logger.Errorf("error while computing feature flags, some flags may be missing or incorrect. Learn more at https://posthog.com/docs/feature-flags/best-practices")
	}

	return &flagsResponse, nil
}

func (d *flagsClient) sleepBeforeFlagsRetry(attempt int) {
	retryAfter := d.retryAfter
	if retryAfter == nil {
		retryAfter = defaultFlagsBackoff().Duration
	}
	if delay := retryAfter(attempt); delay > 0 {
		time.Sleep(delay)
	}
}

func isRetryableFlagsRequestError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return true
	}
	return false
}

func isRetryableFlagsStatusCode(statusCode int) bool {
	return statusCode == http.StatusBadGateway || statusCode == http.StatusGatewayTimeout
}

// rawMessageToString converts a json.RawMessage to a string.
// If the raw message is a JSON string, it returns the unquoted string.
// Otherwise, it returns the JSON representation.
func rawMessageToString(raw json.RawMessage) string {
	n := len(raw)
	if n == 0 {
		return ""
	}
	// Fast path for JSON strings: handle unquoting without json.Unmarshal
	if n >= 2 && raw[0] == '"' && raw[n-1] == '"' {
		inner := raw[1 : n-1]
		// Check for escape sequences
		hasEscape := false
		for _, b := range inner {
			if b == '\\' {
				hasEscape = true
				break
			}
		}
		if !hasEscape {
			// No escapes — direct conversion
			return string(inner)
		}
		// Handle common JSON escape sequences without json.Unmarshal.
		// This avoids 1-2 heap allocations from the JSON decoder.
		return unquoteJSONString(inner)
	}
	// Handle JSON null — return empty string (matches json.Unmarshal behavior for null → string)
	if n == 4 && raw[0] == 'n' && raw[1] == 'u' && raw[2] == 'l' && raw[3] == 'l' {
		return ""
	}
	// Non-string types (objects, arrays, numbers, booleans): return raw JSON representation
	return string(raw)
}

// unquoteJSONString processes JSON escape sequences in a byte slice.
// Handles: \\, \", \/, \n, \r, \t, \b, \f. Falls back to json.Unmarshal for \uXXXX.
func unquoteJSONString(data []byte) string {
	// Check if we need json.Unmarshal for unicode escapes
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\\' && data[i+1] == 'u' {
			// Unicode escape — fall back to json.Unmarshal
			raw := make([]byte, len(data)+2)
			raw[0] = '"'
			copy(raw[1:], data)
			raw[len(raw)-1] = '"'
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				return s
			}
			return string(data)
		}
	}

	// Process simple escape sequences in a single pass
	buf := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '\\' && i+1 < len(data) {
			i++
			switch data[i] {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case '/':
				buf = append(buf, '/')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			default:
				buf = append(buf, '\\', data[i])
			}
		} else {
			buf = append(buf, data[i])
		}
	}
	return string(buf)
}
