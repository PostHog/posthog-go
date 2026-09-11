package posthog

import (
	"fmt"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// capturePath is the analytics capture endpoint. The "v1" in the route is
// the backend's wire-protocol version and is part of the external contract.
const capturePath = "/i/v1/analytics/events"

// Magic event-property keys lifted out of properties into the wire shape.
// propertyProcessPersonProfile and propertySessionID are defined in
// request_context.go; reuse them here.
const (
	propertyCookielessMode = "$cookieless_mode"
	propertyIgnoreSentAt   = "$ignore_sent_at"
	propertyProductTourId  = "$product_tour_id"
)

// Per-event result codes (the only four the backend emits, see
// rust/capture/src/v1/analytics/types.rs EventResult).
const (
	resultOk      = "ok"
	resultWarning = "warning"
	resultDrop    = "drop"
	resultRetry   = "retry"
)

// propertyExtraction defines a magic property that is lifted out of the
// properties map during serialization. If topLevel is true, the value is
// placed into a top-level event field (session_id, window_id); otherwise it
// goes into the options object under wireKey.
//
// For options entries (topLevel=false), coerce validates and normalizes the
// caller's Go value into the type the backend expects (bool or string).
// The magic property is always removed from properties — these sentinel keys
// must never reach backend properties. If coercion fails, the option key
// is omitted (backend applies its default) and a debug log is emitted.
type propertyExtraction struct {
	propKey  string
	wireKey  string
	topLevel bool
	coerce   func(interface{}) (interface{}, bool) // nil = accept as-is (top-level entries)
}

// numericToFloat converts any built-in Go numeric type (or json.Number) to
// float64, mirroring Rust's serde_json Value::Number::as_f64(). Returns
// (0, false) for non-numeric types.
func numericToFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// coerceBool converts a value to bool using the same truthiness rules the
// backend would apply: real bool passes through; common string forms are
// accepted ("true"/"1" → true, "false"/"0" → false); any numeric type
// (int*, uint*, float*, json.Number) coerces via nonzero == true, matching
// posthog-rs's Value::Number arm. Returns (zero, false) when the value is
// not interpretable as a boolean.
func coerceBool(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
		return nil, false
	default:
		if f, ok := numericToFloat(v); ok {
			return f != 0, true
		}
		return nil, false
	}
}

// coerceString accepts only string values. The backend's product_tour_id is
// Option<String>; non-string types are not interpretable.
func coerceString(v interface{}) (interface{}, bool) {
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	return s, true
}

// propertyExtractionTable maps magic event properties to their wire
// destinations. Order mirrors posthog-rs. A key is lifted only when present in
// properties (i.e. the caller overrode a backend default). Options entries
// carry a coerce function matching the backend's expected type; top-level
// entries leave coerce nil.
var propertyExtractionTable = []propertyExtraction{
	{propertyCookielessMode, "cookieless_mode", false, coerceBool},
	{propertyIgnoreSentAt, "disable_skew_correction", false, coerceBool},
	{propertyProductTourId, "product_tour_id", false, coerceString},
	{propertyProcessPersonProfile, "process_person_profile", false, coerceBool},
	{propertySessionID, "session_id", true, nil},
	{propertyWindowID, "window_id", true, nil},
}

// eventBatch is the request envelope. It carries no
// api_key/token (Bearer auth) and no sent_at.
type eventBatch struct {
	CreatedAt           string            `json:"created_at"`
	HistoricalMigration bool              `json:"historical_migration,omitempty"`
	Batch               []json.RawMessage `json:"batch"`
}

// eventPayload is a single wire event. Options is always non-nil so it
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

// captureResponse is the 200 body: a per-uuid map of outcomes.
type captureResponse struct {
	Results map[string]eventResult `json:"results"`
}

// eventResult is a single per-event outcome. Details is optional.
type eventResult struct {
	Result  string  `json:"result"`
	Details *string `json:"details,omitempty"`
}

// captureErrorResponse is the best-effort body parsed from a non-2xx response.
type captureErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorUri         string `json:"error_uri"`
}

// apiEvent is the intermediate, pre-options-extraction view of a message. Each
// Message produces one via apifyEvent; buildEvent turns it into the wire shape.
type apiEvent struct {
	event      string
	uuid       string
	distinctId string
	timestamp  time.Time
	properties Properties
}

// buildEvent extracts magic properties into options or top-level fields and
// returns the wire payload. It mutates e.properties by deleting the lifted keys;
// callers must ensure the properties map is not shared.
//
// Options entries are always removed from properties (these sentinel keys must
// never appear in backend properties) and type-coerced to match the
// backend's strict serde schema. If coercion fails the option key is omitted
// so the backend applies its default. logger may be nil (tests).
func buildEvent(e apiEvent, logger Logger) eventPayload {
	props := e.properties
	if props == nil {
		props = Properties{}
	}
	options := map[string]interface{}{}
	var sessionId, windowId string
	for _, m := range propertyExtractionTable {
		v, ok := props[m.propKey]
		if !ok {
			continue
		}
		if m.topLevel {
			delete(props, m.propKey)
			if s, ok := v.(string); ok {
				switch m.wireKey {
				case "session_id":
					sessionId = s
				case "window_id":
					windowId = s
				}
			}
		} else {
			delete(props, m.propKey)
			if m.coerce == nil {
				options[m.wireKey] = v
				continue
			}
			coerced, ok := m.coerce(v)
			if !ok {
				if logger != nil {
					logger.Debugf("options: dropping %s (uncoercible %T value), backend will apply default", m.propKey, v)
				}
				continue
			}
			options[m.wireKey] = coerced
		}
	}
	return eventPayload{
		Event:      e.event,
		Uuid:       e.uuid,
		DistinctId: e.distinctId,
		Timestamp:  e.timestamp.UTC(),
		SessionId:  sessionId,
		WindowId:   windowId,
		Options:    options,
		Properties: props,
	}
}

// baseProperties returns the common properties shared by all event types.
func baseProperties(isServer bool, disableGeoIP bool) Properties {
	props := Properties{}
	if isServer {
		props.Set("$is_server", true)
	}
	if disableGeoIP {
		props.Set(propertyGeoipDisable, true)
	}
	return props
}

// prepareForSend builds the callback APIMessage, serializes the wire event,
// and returns the event uuid for per-event result correlation. logger may be
// nil (tests).
func prepareForSend(msg Message, logger Logger) (json.RawMessage, APIMessage, string, error) {
	apiMsg := msg.APIfy()
	ev := buildEvent(msg.apifyEvent(), logger)
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, apiMsg, ev.Uuid, err
	}
	return json.RawMessage(data), apiMsg, ev.Uuid, nil
}

// apifyEvent builds the intermediate event for a Capture. It mirrors the
// properties APIfy assembles, minus $lib/$lib_version (the PostHog-Sdk-Info
// header is the authoritative SDK identity).
func (msg Capture) apifyEvent() apiEvent {
	myProperties := baseProperties(msg.IsServer, false).
		Merge(msg.selectedProperties()).
		mergeDefaults(getSystemContext().ToProperties())

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

// apifyEvent builds the intermediate event for an Identify. The person
// properties are folded into properties.$set (there is no top-level $set).
func (msg Identify) apifyEvent() apiEvent {
	myProperties := baseProperties(msg.IsServer, msg.DisableGeoIP).
		mergeDefaults(getSystemContext().ToProperties())

	if msg.Properties != nil {
		myProperties.Set("$set", msg.Properties)
	}

	return apiEvent{
		event:      "$identify",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the intermediate event for a GroupIdentify. The group
// identifiers and $group_set stay in properties (the ingestion groups step reads
// them from there).
func (msg GroupIdentify) apifyEvent() apiEvent {
	myProperties := baseProperties(msg.IsServer, msg.DisableGeoIP).
		Set("$group_type", msg.Type).
		Set("$group_key", msg.Key).
		mergeDefaults(getSystemContext().ToProperties())

	if msg.Properties != nil {
		myProperties.Set("$group_set", msg.Properties)
	}

	return apiEvent{
		event:      "$groupidentify",
		uuid:       msg.Uuid,
		distinctId: fmt.Sprintf("$%s_%s", msg.Type, msg.Key),
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the intermediate event for an Alias. The canonical
// distinct_id is the top-level field; the alias merge reads the
// "alias" property and the top-level distinct_id, so no distinct_id is duplicated
// into properties.
func (msg Alias) apifyEvent() apiEvent {
	myProperties := baseProperties(msg.IsServer, msg.DisableGeoIP).
		mergeDefaults(getSystemContext().ToProperties()).
		Set("alias", msg.Alias)

	return apiEvent{
		event:      "$create_alias",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}

// apifyEvent builds the intermediate event for an Exception. The typed
// exception fields win over custom properties on collision (matching the legacy
// ExceptionInApiProperties marshal precedence).
func (msg Exception) apifyEvent() apiEvent {
	myProperties := baseProperties(msg.IsServer, msg.DisableGeoIP).
		Merge(msg.Properties).
		mergeDefaults(getSystemContext().ToProperties()).
		Set("$exception_list", msg.ExceptionList)

	if msg.ExceptionFingerprint != nil {
		myProperties.Set("$exception_fingerprint", msg.ExceptionFingerprint)
	}
	if len(msg.DebugImages) > 0 {
		myProperties.Set("$debug_images", msg.DebugImages)
	}

	return apiEvent{
		event:      "$exception",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}
