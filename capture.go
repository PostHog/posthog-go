package posthog

import (
	"time"
)

// SendFeatureFlagsValue defines the legacy Capture.SendFeatureFlags configuration interface.
type SendFeatureFlagsValue interface {
	// ShouldSend reports whether Enqueue should attach feature flag values to the Capture event.
	ShouldSend() bool
	// GetOptions returns optional feature flag evaluation parameters, or nil for boolean-only values.
	GetOptions() *SendFeatureFlagsOptions
}

// SendFeatureFlagsOptions allows granular control over legacy feature flag evaluation for Capture.
// Deprecated: Prefer Client.EvaluateFlags and Capture.Flags so captures use the exact
// flag snapshot your application branched on without another /flags request.
type SendFeatureFlagsOptions struct {
	// OnlyEvaluateLocally forces evaluation to use locally loaded flag definitions only.
	OnlyEvaluateLocally bool
	// DeviceId provides a device_id for remote flag evaluation requests.
	DeviceId *string
	// PersonProperties provides explicit person properties for flag evaluation.
	PersonProperties Properties
	// GroupProperties provides explicit group properties for flag evaluation.
	GroupProperties map[string]Properties
}

// ShouldSend reports true when opts is non-nil.
func (opts *SendFeatureFlagsOptions) ShouldSend() bool {
	return opts != nil
}

// GetOptions returns opts so Capture can pass the options to feature flag evaluation.
func (opts *SendFeatureFlagsOptions) GetOptions() *SendFeatureFlagsOptions {
	return opts
}

// SendFeatureFlagsBool wraps a boolean value to implement SendFeatureFlagsValue.
type SendFeatureFlagsBool bool

// ShouldSend reports the wrapped boolean value.
func (b SendFeatureFlagsBool) ShouldSend() bool {
	return bool(b)
}

// GetOptions returns nil because SendFeatureFlagsBool has no additional options.
func (b SendFeatureFlagsBool) GetOptions() *SendFeatureFlagsOptions {
	return nil
}

// SendFeatureFlags converts enabled into a legacy Capture.SendFeatureFlags value.
// Deprecated: Prefer Client.EvaluateFlags and Capture.Flags.
func SendFeatureFlags(enabled bool) SendFeatureFlagsValue {
	return SendFeatureFlagsBool(enabled)
}

// SendFeatureFlagsWithOptions wraps legacy feature flag capture options.
// Deprecated: Prefer Client.EvaluateFlags and Capture.Flags.
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

// Capture represents a custom event to send to PostHog.
// Enqueue validates Event and DistinctId (or obtains DistinctId from request context),
// fills Type, Uuid, and Timestamp, merges Config.DefaultEventProperties, and queues
// the event for a future batch upload.
type Capture struct {
	// Type is reserved for SDK serialization and is overwritten by Enqueue.
	// Deprecated: PostHog ignores the top-level type field on capture events. Use Event for
	// the captured event name.
	Type string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	// If set, it must be a valid UUID; invalid values are replaced with a generated UUID.
	// Set it only when you need idempotency, for example to prevent duplicate events.
	Uuid string
	// DistinctId identifies the user or entity that performed Event.
	// When using EnqueueWithContext, this can be inherited from RequestContext.
	DistinctId string
	// Event is the event name to capture. It must be non-empty.
	Event string
	// Timestamp is the event timestamp. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// Properties are event properties. Enqueue merges request context properties
	// and Config.DefaultEventProperties into this map before sending.
	Properties Properties
	// Groups associates the event with group analytics groups.
	Groups Groups
	// SendFeatureFlags requests legacy feature flag enrichment on this event.
	// Deprecated: Prefer Client.EvaluateFlags and pass the returned snapshot via Flags.
	// Flags writes the canonical $feature/<key> and $active_feature_flags properties.
	SendFeatureFlags SendFeatureFlagsValue
	// Flags, when set, attaches $feature/<key> and $active_feature_flags
	// properties from a snapshot returned by Client.EvaluateFlags. It is
	// preferred over SendFeatureFlags: the snapshot guarantees the event
	// carries the exact values the application branched on and avoids a
	// hidden /flags request on every capture. Flags takes precedence when
	// both are set.
	Flags *FeatureFlagEvaluations
	// IsServer controls whether the event includes the $is_server property.
	// Enqueue overwrites it from Config.GetIsServer.
	IsServer bool
	// minimalFlagCalledEvent marks a $feature_flag_called event for the minimal
	// shape: serialization keeps only the allowlisted evaluation properties and
	// skips system context. It is set only when the server enabled
	// minimal_flag_called_events and the flag has no linked experiment. This is
	// the resolved per-event decision (shouldMinimizeFlagCalledEvent's output),
	// distinct from the plural minimalFlagCalledEvents gate that decision reads.
	minimalFlagCalledEvent bool
}

func (msg Capture) internal() {
	panic(unimplementedError)
}

// Validate checks that the capture message has a non-empty Event and DistinctId.
func (msg Capture) Validate() error {
	return validateRequiredStringFields("posthog.Capture", requiredStringField{name: "Event", value: msg.Event}, requiredStringField{name: "DistinctId", value: msg.DistinctId})
}

func validateCaptureEvent(msg Capture) error {
	return validateRequiredStringFields("posthog.Capture", requiredStringField{name: "Event", value: msg.Event})
}

// CaptureInApi is the wire-format payload produced from a Capture message.
type CaptureInApi struct {
	// Type is the legacy message discriminator retained for callbacks.
	// Deprecated: PostHog ignores this top-level field for capture events, so it
	// is no longer serialized. Use Event for the captured event name.
	Type string `json:"-"`
	// Uuid is the valid event UUID sent to the batch API.
	Uuid string `json:"uuid"`
	// Library is the legacy top-level SDK name retained for callbacks.
	// Deprecated: PostHog reads SDK identity from Properties["$lib"], so this
	// top-level field is no longer serialized.
	Library string `json:"-"`
	// LibraryVersion is the legacy top-level SDK version retained for callbacks.
	// Deprecated: PostHog reads SDK version from Properties["$lib_version"], so
	// this top-level field is no longer serialized.
	LibraryVersion string `json:"-"`
	// Timestamp is the event timestamp sent to the batch API.
	Timestamp time.Time `json:"timestamp"`

	// DistinctId identifies the user or entity for the event.
	DistinctId string `json:"distinct_id"`
	// Event is the captured event name.
	Event string `json:"event"`
	// Properties contains event, SDK, system, group, and feature flag properties.
	Properties Properties `json:"properties"`
	// SendFeatureFlags carries the legacy send_feature_flags value for callbacks.
	// Deprecated: PostHog ignores this top-level field, so it is no longer
	// serialized. Use Capture.Flags to send the canonical $feature/<key> and
	// $active_feature_flags properties.
	SendFeatureFlags SendFeatureFlagsValue `json:"-"`
}

// minimalFlagCalledEventAllowlist lists the only event properties kept on a
// minimal $feature_flag_called event, per the cross-SDK contract. Everything
// else — Config.DefaultEventProperties and request-context properties
// included — is stripped so the minimal shape stays predictable.
// $geoip_disable is kept because, like $process_person_profile, it is a
// processing-control sentinel: stripping it would silently re-enable GeoIP
// enrichment for events from clients that disabled it. $session_id,
// $window_id, and $device_id are linkage identifiers the contract preserves.
// $is_server is kept so server-event classification still works. System
// context ($os, $os_version, $os_distro, $go_version) isn't filtered through
// this allowlist — APIfy merges it into minimal events the same way it does
// for full events, since those are cheap, low-cardinality dimensions kept for
// platform/runtime breakdowns on flag-call debugging.
var minimalFlagCalledEventAllowlist = []string{
	"$feature_flag",
	"$feature_flag_response",
	"$feature_flag_has_experiment",
	"$feature_flag_id",
	"$feature_flag_version",
	"$feature_flag_reason",
	"$feature_flag_request_id",
	"$feature_flag_evaluated_at",
	"$feature_flag_error",
	"locally_evaluated",
	// $groups is listed for cross-SDK contract parity even though it currently
	// has no effect here: APIfy/apifyEvent set it from Capture.Groups after
	// this allowlist runs, not from a raw "$groups" key in Properties.
	"$groups",
	propertyProcessPersonProfile,
	propertyGeoipDisable,
	propertyIsServer,
	propertySessionID,
	propertyWindowID,
	"$device_id",
}

// minimalFlagCalledEventProperties builds a fresh property set containing only
// the allowlisted minimal $feature_flag_called properties present in props.
func minimalFlagCalledEventProperties(props Properties) Properties {
	minimal := NewProperties()
	for _, key := range minimalFlagCalledEventAllowlist {
		if value, ok := props[key]; ok {
			minimal[key] = value
		}
	}
	return minimal
}

// shouldMinimizeFlagCalledEvent reports whether a $feature_flag_called event
// should use the minimal shape: the server-controlled gate must be on and the
// flag must be known to have no linked experiment. Any missing signal keeps
// the full event shape.
func shouldMinimizeFlagCalledEvent(minimalFlagCalledEvents bool, hasExperiment *bool) bool {
	return minimalFlagCalledEvents && hasExperiment != nil && !*hasExperiment
}

// selectedProperties returns the source properties for serialization: the
// allowlisted minimal subset when minimalFlagCalledEvent is set, or the full
// set otherwise.
func (msg Capture) selectedProperties() Properties {
	if msg.minimalFlagCalledEvent {
		return minimalFlagCalledEventProperties(msg.Properties)
	}
	return msg.Properties
}

// APIfy converts a Capture message into the PostHog batch API representation.
func (msg Capture) APIfy() APIMessage {
	libraryVersion := getVersion()

	myProperties := Properties{}.
		Merge(msg.selectedProperties()).
		Set("$lib", SDKName).
		Set("$lib_version", libraryVersion).
		Merge(getSystemContext().ToProperties())

	if msg.IsServer {
		myProperties.Set("$is_server", true)
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
