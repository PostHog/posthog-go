package posthog

import (
	"fmt"
	"time"

	json "github.com/goccy/go-json"
)

// captureV1Path is the capture-v1 analytics batch endpoint.
const captureV1Path = "/i/v1/analytics/events"

// Magic event-property keys lifted out of properties into the v1 wire shape.
// propertyProcessPersonProfile and propertySessionID are defined in
// request_context.go; reuse them here.
const (
	propertyCookielessMode = "$cookieless_mode"
	propertyIgnoreSentAt   = "$ignore_sent_at"
	propertyProductTourId  = "$product_tour_id"
	propertyWindowID       = "$window_id"
)

// v1 per-event result codes (the only four the backend emits, see
// rust/capture/src/v1/analytics/types.rs EventResult).
const (
	resultOk      = "ok"
	resultWarning = "warning"
	resultDrop    = "drop"
	resultRetry   = "retry"
)

// optionsExtractionTable maps a magic event property to its v1 options wire key.
// Order mirrors posthog-rs OPTIONS_EXTRACTION_TABLE. A key is lifted only when
// present in properties (i.e. the caller overrode a backend default).
var optionsExtractionTable = []struct{ propKey, wireKey string }{
	{propertyCookielessMode, "cookieless_mode"},
	{propertyIgnoreSentAt, "disable_skew_correction"},
	{propertyProductTourId, "product_tour_id"},
	{propertyProcessPersonProfile, "process_person_profile"},
}

// eventBatch is the v1 request envelope. Unlike the legacy batch it carries no
// api_key/token (Bearer auth) and no sent_at.
type eventBatch struct {
	CreatedAt           string            `json:"created_at"`
	HistoricalMigration bool              `json:"historical_migration,omitempty"`
	Batch               []json.RawMessage `json:"batch"`
}

// eventPayload is a single v1 wire event. Options is always non-nil so it
// renders as "{}" rather than null when empty.
type eventPayload struct {
	Event      string                 `json:"event"`
	Uuid       string                 `json:"uuid"`
	DistinctId string                 `json:"distinct_id"`
	Timestamp  time.Time              `json:"timestamp"`
	SessionId  string                 `json:"session_id,omitempty"`
	WindowId   string                 `json:"window_id,omitempty"`
	Options    map[string]interface{} `json:"options"`
	Properties Properties             `json:"properties"`
}

// captureV1Response is the 200 body: a per-uuid map of outcomes.
type captureV1Response struct {
	Results map[string]eventResult `json:"results"`
}

// eventResult is a single per-event outcome. Details is optional.
type eventResult struct {
	Result  string  `json:"result"`
	Details *string `json:"details,omitempty"`
}

// v1ErrorResponse is the best-effort body parsed from a non-2xx response.
type v1ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorUri         string `json:"error_uri"`
}

// apiEvent is the intermediate, pre-options-extraction view of a message. Each
// Message produces one via apifyEvent; buildV1Event turns it into the wire shape.
type apiEvent struct {
	event      string
	uuid       string
	distinctId string
	timestamp  time.Time
	properties Properties
}

// buildV1Event extracts magic properties into options, lifts session/window IDs
// to top-level fields, and returns the wire payload. It mutates e.properties by
// deleting the lifted keys.
func buildV1Event(e apiEvent) eventPayload {
	props := e.properties
	if props == nil {
		props = Properties{}
	}
	options := map[string]interface{}{}
	for _, m := range optionsExtractionTable {
		if v, ok := props[m.propKey]; ok {
			options[m.wireKey] = v
			delete(props, m.propKey)
		}
	}
	return eventPayload{
		Event:      e.event,
		Uuid:       e.uuid,
		DistinctId: e.distinctId,
		Timestamp:  e.timestamp,
		SessionId:  popString(props, propertySessionID),
		WindowId:   popString(props, propertyWindowID),
		Options:    options,
		Properties: props,
	}
}

// popString removes key from props and returns it as a string ("" if absent or
// not a string).
func popString(props Properties, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	delete(props, key)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// prepareForSendV1 is the v1 sibling of prepareForSend: it builds the callback
// APIMessage (unchanged legacy shape), serializes the v1 wire event, and returns
// the event uuid for result correlation.
func prepareForSendV1(msg Message) (json.RawMessage, APIMessage, string, error) {
	apiMsg := msg.APIfy()
	ev := buildV1Event(msg.apifyEvent())
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, apiMsg, ev.Uuid, err
	}
	return json.RawMessage(data), apiMsg, ev.Uuid, nil
}

// apifyEvent builds the v1 intermediate event for a Capture. It mirrors the
// properties APIfy assembles, minus $lib/$lib_version (the PostHog-Sdk-Info
// header is the authoritative SDK identity in v1).
func (msg Capture) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(msg.Properties).
		Merge(getSystemContext().ToProperties())

	if msg.IsServer {
		myProperties.Set("$is_server", true)
	}
	if msg.Groups != nil {
		myProperties.Set("$groups", msg.Groups)
	}

	return apiEvent{
		event:      msg.Event,
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the v1 intermediate event for an Identify. The person
// properties are folded into properties.$set (v1 has no top-level $set).
func (msg Identify) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(getSystemContext().ToProperties())

	if msg.IsServer {
		myProperties.Set("$is_server", true)
	}
	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}
	myProperties.Set("$set", msg.Properties)

	return apiEvent{
		event:      "$identify",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the v1 intermediate event for a GroupIdentify. The group
// identifiers and $group_set stay in properties (the ingestion groups step reads
// them from there).
func (msg GroupIdentify) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Set("$group_type", msg.Type).
		Set("$group_key", msg.Key).
		Set("$group_set", msg.Properties).
		Merge(getSystemContext().ToProperties())

	if msg.IsServer {
		myProperties.Set("$is_server", true)
	}
	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	return apiEvent{
		event:      "$groupidentify",
		uuid:       msg.Uuid,
		distinctId: fmt.Sprintf("$%s_%s", msg.Type, msg.Key),
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the v1 intermediate event for an Alias. The canonical
// distinct_id is the top-level field (v1 requirement); the alias merge reads the
// "alias" property and the top-level distinct_id, so no distinct_id is duplicated
// into properties.
func (msg Alias) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(getSystemContext().ToProperties()).
		Set("alias", msg.Alias)

	if msg.IsServer {
		myProperties.Set("$is_server", true)
	}
	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	return apiEvent{
		event:      "$create_alias",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the v1 intermediate event for an Exception. The typed
// exception fields win over custom properties on collision (matching the legacy
// ExceptionInApiProperties marshal precedence).
func (msg Exception) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(msg.Properties).
		Merge(getSystemContext().ToProperties()).
		Set("$exception_list", msg.ExceptionList)

	if msg.ExceptionFingerprint != nil {
		myProperties.Set("$exception_fingerprint", msg.ExceptionFingerprint)
	}
	if msg.IsServer {
		myProperties.Set("$is_server", true)
	}
	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	return apiEvent{
		event:      "$exception",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}
