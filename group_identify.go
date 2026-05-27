package posthog

import (
	"fmt"
	"time"
)

// GroupIdentify represents a group analytics call that sets properties on a group.
// Enqueue validates Type and Key, fills Uuid, Timestamp, and DisableGeoIP, then
// sends the message as a $groupidentify event.
type GroupIdentify struct {
	// Type is the group type, such as "company" or "organization".
	Type string
	// Key is the group key or ID within the group type.
	Key string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	Uuid string

	// DistinctId is accepted for compatibility but the wire payload uses a generated group distinct ID.
	DistinctId string
	// Timestamp is the event timestamp. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// Properties are sent as the $group_set properties for the group.
	Properties Properties
	// DisableGeoIP controls whether this group-identify event disables GeoIP lookup.
	// Enqueue overwrites it from Config.GetDisableGeoIP.
	DisableGeoIP bool
}

func (msg GroupIdentify) internal() {
	panic(unimplementedError)
}

// Validate checks that the group identify message has Type and Key set.
func (msg GroupIdentify) Validate() error {
	if len(msg.Type) == 0 {
		return FieldError{
			Type:  "posthog.GroupIdentify",
			Name:  "Type",
			Value: msg.Type,
		}
	}

	if len(msg.Key) == 0 {
		return FieldError{
			Type:  "posthog.GroupIdentify",
			Name:  "Key",
			Value: msg.Key,
		}
	}

	return nil
}

// GroupIdentifyInApi is the wire-format payload produced from a GroupIdentify message.
type GroupIdentifyInApi struct {
	// Uuid is the event UUID sent to the batch API.
	Uuid string `json:"uuid"`
	// Library is the SDK name sent to the batch API.
	Library string `json:"library"`
	// LibraryVersion is the SDK version sent to the batch API.
	LibraryVersion string `json:"library_version"`
	// Timestamp is the event timestamp sent to the batch API.
	Timestamp time.Time `json:"timestamp"`

	// Event is always $groupidentify for GroupIdentify messages.
	Event string `json:"event"`
	// DistinctId is the generated group distinct ID sent to PostHog.
	DistinctId string `json:"distinct_id"`
	// Properties contains SDK metadata, group identifiers, and $group_set properties.
	Properties Properties `json:"properties"`
}

// APIfy converts a GroupIdentify message into the PostHog batch API representation.
func (msg GroupIdentify) APIfy() APIMessage {
	myProperties := Properties{}.
		Set("$lib", SDKName).
		Set("$lib_version", getVersion()).
		Set("$group_type", msg.Type).
		Set("$group_key", msg.Key).
		Set("$group_set", msg.Properties).
		Merge(getSystemContext().ToProperties())

	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	distinctId := fmt.Sprintf("$%s_%s", msg.Type, msg.Key)

	apified := GroupIdentifyInApi{
		Uuid:           msg.Uuid,
		Event:          "$groupidentify",
		Properties:     myProperties,
		DistinctId:     distinctId,
		Timestamp:      msg.Timestamp,
		Library:        SDKName,
		LibraryVersion: getVersion(),
	}

	return apified
}
