package posthog

import (
	"time"
)

var _ Message = (*Alias)(nil)

// Alias represents an alias call that links another distinct ID to an existing user.
// Enqueue validates that DistinctId and Alias are both set, fills Type, Uuid,
// Timestamp, and DisableGeoIP, then sends the message as a $create_alias event.
type Alias struct {
	// Type is reserved for SDK serialization and is overwritten by Enqueue.
	Type string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	Uuid string

	// Alias is the alternate distinct ID to attach to DistinctId.
	Alias string
	// DistinctId is the existing user distinct ID that Alias should resolve to.
	DistinctId string
	// Timestamp is the event timestamp. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// DisableGeoIP controls whether this alias event disables GeoIP lookup.
	// Enqueue overwrites it from Config.GetDisableGeoIP.
	DisableGeoIP bool
	// IsServer controls whether the event includes the $is_server property.
	// Enqueue overwrites it from Config.GetIsServer.
	IsServer bool
}

func (msg Alias) internal() {
	panic(unimplementedError)
}

// Validate checks that the alias message has both DistinctId and Alias set.
func (msg Alias) Validate() error {
	if len(msg.DistinctId) == 0 {
		return FieldError{
			Type:  "posthog.Alias",
			Name:  "DistinctId",
			Value: msg.DistinctId,
		}
	}

	if len(msg.Alias) == 0 {
		return FieldError{
			Type:  "posthog.Alias",
			Name:  "Alias",
			Value: msg.Alias,
		}
	}

	return nil
}

// AliasInApiProperties is the wire-format properties object for an Alias message.
type AliasInApiProperties struct {
	sysContext
	// DistinctId is the canonical user distinct ID.
	DistinctId string `json:"distinct_id"`
	// Alias is the alternate distinct ID being linked to DistinctId.
	Alias string `json:"alias"`
	// Lib is the SDK name sent as $lib.
	Lib string `json:"$lib"`
	// LibVersion is the SDK version sent as $lib_version.
	LibVersion string `json:"$lib_version"`
	// IsServer marks the event as originating from a server-side SDK.
	// Omitted entirely when nil (Config.IsServer resolved to false).
	IsServer *bool `json:"$is_server,omitempty"`
	// DisableGeoIP is sent as $geoip_disable when GeoIP lookup is disabled.
	DisableGeoIP bool `json:"$geoip_disable,omitempty"`
}

// AliasInApi is the wire-format payload produced from an Alias message.
type AliasInApi struct {
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

	// Properties contains alias-specific event properties.
	Properties AliasInApiProperties `json:"properties"`

	// Event is always $create_alias for Alias messages.
	Event string `json:"event"`
}

// APIfy converts an Alias message into the PostHog batch API representation.
func (msg Alias) APIfy() APIMessage {
	libraryVersion := getVersion()

	var isServer *bool
	if msg.IsServer {
		isServer = Ptr(true)
	}

	apified := AliasInApi{
		Type:           msg.Type,
		Uuid:           msg.Uuid,
		Event:          "$create_alias",
		Library:        SDKName,
		LibraryVersion: libraryVersion,
		Timestamp:      msg.Timestamp,
		Properties: AliasInApiProperties{
			sysContext:   getSystemContext(),
			DistinctId:   msg.DistinctId,
			Alias:        msg.Alias,
			Lib:          SDKName,
			LibVersion:   libraryVersion,
			IsServer:     isServer,
			DisableGeoIP: msg.DisableGeoIP,
		},
	}

	return apified
}
