package posthog

import (
	"time"
)

var _ Message = (*Identify)(nil)

// Identify represents an identify call that sets person properties for a user.
// Enqueue validates that DistinctId is set, fills Type, Uuid, Timestamp, and
// DisableGeoIP, then sends the message as a $identify event.
type Identify struct {
	// Type is reserved for SDK serialization and is overwritten by Enqueue.
	Type string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	Uuid string

	// DistinctId is the user distinct ID whose person profile should be updated.
	DistinctId string
	// Timestamp is the event timestamp. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// Properties are sent as the $set person properties for DistinctId.
	Properties Properties
	// DisableGeoIP controls whether this identify event disables GeoIP lookup.
	// Enqueue overwrites it from Config.GetDisableGeoIP.
	DisableGeoIP bool
}

func (msg Identify) internal() {
	panic(unimplementedError)
}

// Validate checks that the identify message has a DistinctId.
func (msg Identify) Validate() error {
	if len(msg.DistinctId) == 0 {
		return FieldError{
			Type:  "posthog.Identify",
			Name:  "DistinctId",
			Value: msg.DistinctId,
		}
	}

	return nil
}

// IdentifyInApi is the wire-format payload produced from an Identify message.
type IdentifyInApi struct {
	// Type is the message type sent to the batch API.
	Type string `json:"type"`
	// Uuid is the event UUID sent to the batch API.
	Uuid string `json:"uuid"`
	// Library is the SDK name sent to the batch API.
	Library string `json:"library"`
	// LibraryVersion is the SDK version sent to the batch API.
	LibraryVersion string `json:"library_version"`
	// Timestamp is the event timestamp sent to the batch API.
	Timestamp time.Time `json:"timestamp"`

	// Event is always $identify for Identify messages.
	Event string `json:"event"`
	// DistinctId is the user distinct ID sent to PostHog.
	DistinctId string `json:"distinct_id"`
	// Properties contains SDK metadata and system context.
	Properties Properties `json:"properties"`
	// Set contains the person properties to update.
	Set Properties `json:"$set"`
}

// APIfy converts an Identify message into the PostHog batch API representation.
func (msg Identify) APIfy() APIMessage {
	myProperties := Properties{}.
		Set("$lib", SDKName).
		Set("$lib_version", getVersion()).
		Set("$is_server", true).
		Merge(getSystemContext().ToProperties())

	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	apified := IdentifyInApi{
		Type:           msg.Type,
		Uuid:           msg.Uuid,
		Event:          "$identify",
		Library:        SDKName,
		LibraryVersion: getVersion(),
		Timestamp:      msg.Timestamp,
		DistinctId:     msg.DistinctId,

		Properties: myProperties,
		Set:        msg.Properties,
	}

	return apified
}
