package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type DecideRequestData struct {
	ApiKey           string                `json:"api_key"`
	DistinctId       string                `json:"distinct_id"`
	Groups           Groups                `json:"groups"`
	PersonProperties Properties            `json:"person_properties"`
	GroupProperties  map[string]Properties `json:"group_properties"`
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
	ConditionIndex int    `json:"condition_index"`
}

// FlagMetadata contains additional information about a flag
type FlagMetadata struct {
	ID          int     `json:"id"`
	Version     int     `json:"version"`
	Payload     *string `json:"payload"`
	Description *string `json:"description,omitempty"`
}

// GetValue returns the variant if it exists, otherwise returns the enabled status
func (f FlagDetail) GetValue() interface{} {
	if f.Variant != nil {
		return *f.Variant
	}
	return f.Enabled
}

// NewFlagDetail creates a new FlagDetail from a key, value, and optional payload
func NewFlagDetail(key string, value interface{}, payload *string) FlagDetail {
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

// DecideResponse represents the response from the decide endpoint v3 or v4.
// It is a normalized super set of the v3 and v4 formats.
type DecideResponse struct {
	CommonResponseFields

	// v4 flags format
	Flags map[string]FlagDetail `json:"flags,omitempty"`

	// v3 legacy fields
	FeatureFlags        map[string]interface{} `json:"featureFlags"`
	FeatureFlagPayloads map[string]string      `json:"featureFlagPayloads"`
}

// CommonResponseFields contains fields common to all decide response versions
type CommonResponseFields struct {
	QuotaLimited              *[]string `json:"quota_limited"`
	RequestId                 string    `json:"requestId"`
	ErrorsWhileComputingFlags bool      `json:"errorsWhileComputingFlags"`
}

// UnmarshalJSON implements custom unmarshaling to handle both v3 and v4 formats
func (r *DecideResponse) UnmarshalJSON(data []byte) error {
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
		r.FeatureFlagPayloads = make(map[string]string)
		for key, flag := range r.Flags {
			r.FeatureFlags[key] = flag.GetValue()
			if flag.Metadata.Payload != nil {
				r.FeatureFlagPayloads[key] = *flag.Metadata.Payload
			}
		}
		return nil
	}

	// If not v4, try v3 format
	type V3Response struct {
		CommonResponseFields
		FeatureFlags        map[string]interface{} `json:"featureFlags"`
		FeatureFlagPayloads map[string]string      `json:"featureFlagPayloads"`
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
		var payloadPtr *string
		if payload, ok := v3.FeatureFlagPayloads[key]; ok {
			payloadPtr = &payload
		}
		r.Flags[key] = NewFlagDetail(key, value, payloadPtr)
	}
	return nil
}

// decider defines the interface for making decide requests
type decider interface {
	makeDecideRequest(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (*DecideResponse, error)
}

// decideClient implements the decider interface
type decideClient struct {
	apiKey                    string
	endpoint                  string
	http                      http.Client
	featureFlagRequestTimeout time.Duration
	errorf                    func(format string, args ...interface{})
}

// newDecideClient creates a new decideClient
func newDecideClient(apiKey string, endpoint string, httpClient http.Client, featureFlagRequestTimeout time.Duration, errorf func(format string, args ...interface{})) *decideClient {
	return &decideClient{
		apiKey:                    apiKey,
		endpoint:                  endpoint,
		http:                      httpClient,
		featureFlagRequestTimeout: featureFlagRequestTimeout,
		errorf:                    errorf,
	}
}

// makeDecideRequest makes a request to the decide endpoint and deserializes the response
// into a DecideResponse struct.
func (d *decideClient) makeDecideRequest(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (*DecideResponse, error) {
	requestData := DecideRequestData{
		ApiKey:           d.apiKey,
		DistinctId:       distinctId,
		Groups:           groups,
		PersonProperties: personProperties,
		GroupProperties:  groupProperties,
	}

	requestDataBytes, err := json.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal decide endpoint request data: %v", err)
	}

	// Try v4 endpoint first
	decideEndpoint := "decide/?v=4"
	url, err := url.Parse(d.endpoint + "/" + decideEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating url: %v", err)
	}

	req, err := http.NewRequest("POST", url.String(), bytes.NewReader(requestDataBytes))
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
		return nil, fmt.Errorf("unexpected status code from /decide/: %d", res.StatusCode)
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from /decide/: %v", err)
	}

	var decideResponse DecideResponse
	err = json.Unmarshal(resBody, &decideResponse)
	if err != nil {
		return nil, fmt.Errorf("error parsing response from /decide/: %v", err)
	}

	if decideResponse.ErrorsWhileComputingFlags {
		d.errorf("error while computing feature flags, some flags may be missing or incorrect. Learn more at https://posthog.com/docs/feature-flags/best-practices")
	}

	return &decideResponse, nil
}
