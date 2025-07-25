package posthog

import "time"

// SendFeatureFlagsValue defines the interface for feature flag configuration
type SendFeatureFlagsValue interface {
	ShouldSend() bool
	GetOptions() *SendFeatureFlagsOptions
}

// SendFeatureFlagsOptions allows for more granular control over feature flag evaluation
type SendFeatureFlagsOptions struct {
	// OnlyEvaluateLocally forces evaluation to only use local flags and never make API requests
	OnlyEvaluateLocally bool
	// PersonProperties provides explicit person properties for local flag evaluation
	PersonProperties Properties
	// GroupProperties provides explicit group properties for local flag evaluation
	GroupProperties map[string]Properties
}

// Implement SendFeatureFlagsValue interface for SendFeatureFlagsOptions
func (opts *SendFeatureFlagsOptions) ShouldSend() bool {
	return opts != nil
}

func (opts *SendFeatureFlagsOptions) GetOptions() *SendFeatureFlagsOptions {
	return opts
}

// SendFeatureFlagsBool wraps a boolean value to implement SendFeatureFlagsValue
type SendFeatureFlagsBool bool

// Implement SendFeatureFlagsValue interface for SendFeatureFlagsBool
func (b SendFeatureFlagsBool) ShouldSend() bool {
	return bool(b)
}

func (b SendFeatureFlagsBool) GetOptions() *SendFeatureFlagsOptions {
	return nil
}

// Constructor functions for easier usage
func SendFeatureFlags(enabled bool) SendFeatureFlagsValue {
	return SendFeatureFlagsBool(enabled)
}

func SendFeatureFlagsWithOptions(opts *SendFeatureFlagsOptions) SendFeatureFlagsValue {
	return opts
}

// Helper functions to work with SendFeatureFlags interface
func (c *Capture) shouldSendFeatureFlags() bool {
	if c.SendFeatureFlags == nil {
		return false
	}
	return c.SendFeatureFlags.ShouldSend()
}

func (c *Capture) getFeatureFlagsOptions() *SendFeatureFlagsOptions {
	if c.SendFeatureFlags == nil {
		return nil
	}
	return c.SendFeatureFlags.GetOptions()
}

var _ Message = (*Capture)(nil)

// This type represents object sent in a capture call
type Capture struct {
	// This field is exported for serialization purposes and shouldn't be set by
	// the application, its value is always overwritten by the library.
	Type string
	// You don't usually need to specify this field - Posthog will generate it automatically.
	// Use it only when necessary - for example, to prevent duplicate events.
	Uuid             string
	DistinctId       string
	Event            string
	Timestamp        time.Time
	Properties       Properties
	Groups           Groups
	SendFeatureFlags SendFeatureFlagsValue
}

func (msg Capture) internal() {
	panic(unimplementedError)
}

func (msg Capture) Validate() error {
	if len(msg.Event) == 0 {
		return FieldError{
			Type:  "posthog.Capture",
			Name:  "Event",
			Value: msg.Event,
		}
	}

	if len(msg.DistinctId) == 0 {
		return FieldError{
			Type:  "posthog.Capture",
			Name:  "DistinctId",
			Value: msg.DistinctId,
		}
	}

	return nil
}

type CaptureInApi struct {
	Type           string    `json:"type"`
	Uuid           string    `json:"uuid"`
	Library        string    `json:"library"`
	LibraryVersion string    `json:"library_version"`
	Timestamp      time.Time `json:"timestamp"`

	DistinctId       string                `json:"distinct_id"`
	Event            string                `json:"event"`
	Properties       Properties            `json:"properties"`
	SendFeatureFlags SendFeatureFlagsValue `json:"send_feature_flags"`
}

func (msg Capture) APIfy() APIMessage {
	libraryVersion := getVersion()

	myProperties := Properties{}.Set("$lib", SDKName).Set("$lib_version", libraryVersion)

	if msg.Properties != nil {
		for k, v := range msg.Properties {
			myProperties[k] = v
		}
	}

	if msg.Groups != nil {
		myProperties.Set("$groups", msg.Groups)
	}

	apified := CaptureInApi{
		Type:             msg.Type,
		Uuid:             msg.Uuid,
		Library:          SDKName,
		LibraryVersion:   libraryVersion,
		Timestamp:        msg.Timestamp,
		DistinctId:       msg.DistinctId,
		Event:            msg.Event,
		Properties:       myProperties,
		SendFeatureFlags: msg.SendFeatureFlags,
	}

	return apified
}
