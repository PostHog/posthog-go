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
	// Deprecated: this field is ignored by PostHog ingestion and is kept for backwards compatibility.
	Type string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	// If set, it must be a valid UUID; invalid values are replaced with a generated UUID.
	Uuid string

	// Alias is the alternate distinct ID to attach to DistinctId.
	Alias string
	// DistinctId is the existing user distinct ID that Alias should resolve to.
	DistinctId string
	// Timestamp is the event timestamp. UTC is preferred; non-UTC values are
	// converted to the equivalent UTC instant. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// EventProperties are properties of the $create_alias event. Enqueue fills
	// the keys they leave unset from Config.DefaultEventProperties before
	// BeforeSend. The SDK's alias property wins over an "alias" key in them.
	// The Config.Callback message does not include them.
	EventProperties Properties
	// Options are per-event capture options, sent unchanged. See Options.
	Options Options
	// DisableGeoIP reports whether the event sets $geoip_disable. Enqueue sets
	// it from Config.GetDisableGeoIP, then from the $geoip_disable value left
	// after BeforeSend. To change $geoip_disable, set the property.
	DisableGeoIP bool
	// IsServer reports whether the event sets $is_server. Enqueue sets it from
	// Config.GetIsServer, then from the $is_server value left after BeforeSend.
	// To change $is_server, set the property.
	IsServer bool
}

func (msg Alias) internal() {
	panic(unimplementedError)
}

// Validate checks that the alias message has both DistinctId and Alias set.
func (msg Alias) Validate() error {
	return validateRequiredStringFields("posthog.Alias", requiredStringField{name: "DistinctId", value: msg.DistinctId}, requiredStringField{name: "Alias", value: msg.Alias})
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
	// Type is the legacy message type sent to the batch API.
	// Deprecated: this field is ignored by PostHog ingestion and is kept for backwards compatibility.
	Type string `json:"type"`
	// Uuid is the valid event UUID sent to the batch API.
	Uuid string `json:"uuid"`
	// Library is the SDK name sent to the batch API.
	Library string `json:"library"`
	// LibraryVersion is the SDK version sent to the batch API.
	LibraryVersion string `json:"library_version"`
	// Timestamp is the event timestamp sent to the batch API in UTC.
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
		Timestamp:      msg.Timestamp.UTC(),
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
