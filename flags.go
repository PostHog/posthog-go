package posthog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	json "github.com/goccy/go-json"
)

type FlagsRequestData struct {
	ApiKey           string                `json:"api_key"`
	DistinctId       string                `json:"distinct_id"`
	Groups           Groups                `json:"groups"`
	PersonProperties Properties            `json:"person_properties"`
	GroupProperties  map[string]Properties `json:"group_properties"`
	DisableGeoIP     bool                  `json:"geoip_disable,omitempty"`
}

// FlagDetail represents a feature flag in v4 format
type FlagDetail struct {
	Key      string       `json:"key"`
	Enabled  bool         `json:"enabled"`
	Variant  *string      `json:"variant"`
	Reason   *FlagReason  `json:"reason"`
	Metadata FlagMetadata `json:"metadata"`
}

// FlagReason represents why a flag was enabled/disabled
type FlagReason struct {
	Code           string `json:"code"`
	Description    string `json:"description"`
	ConditionIndex *int   `json:"condition_index"`
}

// FlagMetadata contains additional information about a flag
type FlagMetadata struct {
	ID          int             `json:"id"`
	Version     int             `json:"version"`
	Payload     json.RawMessage `json:"payload"`
	Description *string         `json:"description,omitempty"`
}

// GetValue returns the variant if it exists, otherwise returns the enabled status
func (f FlagDetail) GetValue() interface{} {
	if f.Variant != nil {
		return *f.Variant
	}
	return f.Enabled
}

// NewFlagDetail creates a new FlagDetail from a key, value, and optional payload
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

// FlagsResponse represents the response from the flags endpoint v1 or v2.
// It is a normalized super set of the v1 and v2 formats.
type FlagsResponse struct {
	CommonResponseFields

	// v4 flags format
	Flags map[string]FlagDetail `json:"flags,omitempty"`

	// v3 legacy fields
	FeatureFlags        map[string]interface{}     `json:"featureFlags"`
	FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
}

// CommonResponseFields contains fields common to all flags response versions
type CommonResponseFields struct {
	QuotaLimited              []string `json:"quota_limited"`
	RequestId                 string   `json:"requestId"`
	EvaluatedAt               *int64   `json:"evaluatedAt"`
	ErrorsWhileComputingFlags bool     `json:"errorsWhileComputingFlags"`
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
	makeFlagsRequest(distinctId string, groups Groups, personProperties Properties,
		groupProperties map[string]Properties, disableGeoIP bool) (*FlagsResponse, error)
}

// flagsClient implements the decider interface
type flagsClient struct {
	apiKey                    string
	endpoint                  string
	http                      http.Client
	featureFlagRequestTimeout time.Duration
	logger                    Logger
}

// newFlagsClient creates a new flagsClient
func newFlagsClient(apiKey string, endpoint string, httpClient http.Client,
	featureFlagRequestTimeout time.Duration, logger Logger) (*flagsClient, error) {

	// Try v2 endpoint first
	flagsEndpoint := "flags/?v=2"
	flagsEndpointURL, err := url.Parse(endpoint + "/" + flagsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating url: %v", err)
	}

	return &flagsClient{
		apiKey:                    apiKey,
		endpoint:                  flagsEndpointURL.String(),
		http:                      httpClient,
		featureFlagRequestTimeout: featureFlagRequestTimeout,
		logger:                    logger,
	}, nil
}

// makeFlagsRequest makes a request to the flags endpoint and deserializes the response
// into a FlagsResponse struct.
func (d *flagsClient) makeFlagsRequest(distinctId string, groups Groups, personProperties Properties,
	groupProperties map[string]Properties, disableGeoIP bool) (*FlagsResponse, error) {
	requestData := FlagsRequestData{
		ApiKey:           d.apiKey,
		DistinctId:       distinctId,
		Groups:           groups,
		PersonProperties: personProperties,
		GroupProperties:  groupProperties,
		DisableGeoIP:     disableGeoIP,
	}

	requestDataBytes, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal flags endpoint request data: %v", err)
	}

	req, err := http.NewRequest("POST", d.endpoint, bytes.NewReader(requestDataBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "posthog-go/"+Version)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), d.featureFlagRequestTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	res, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, NewAPIError(res.StatusCode, fmt.Sprintf("unexpected status code from /flags/: %d", res.StatusCode))
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from /flags/: %v", err)
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

// rawMessageToString converts a json.RawMessage to a string.
// If the raw message is a JSON string, it returns the unquoted string.
// Otherwise, it returns the JSON representation.
func rawMessageToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try to unmarshal as a string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Return raw JSON for objects, arrays, numbers, booleans
	return string(raw)
}
