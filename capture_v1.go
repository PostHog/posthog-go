package posthog

import (
	"fmt"
	"strings"
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
)

// v1 per-event result codes (the only four the backend emits, see
// rust/capture/src/v1/analytics/types.rs EventResult).
const (
	resultOk      = "ok"
	resultWarning = "warning"
	resultDrop    = "drop"
	resultRetry   = "retry"
)

// propertyExtraction defines a magic property that is lifted out of the
// properties map during v1 serialization. If topLevel is true, the value is
// placed into a top-level event field (session_id, window_id); otherwise it
// goes into the options object under wireKey.
//
// For options entries (topLevel=false), coerce validates and normalizes the
// caller's Go value into the type the v1 backend expects (bool or string).
// The magic property is always removed from properties — these sentinel keys
// must never reach v1 backend properties. If coercion fails, the option key
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

// propertyExtractionTable maps magic event properties to their v1 wire
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

// buildV1Event extracts magic properties into options or top-level fields and
// returns the wire payload. It mutates e.properties by deleting the lifted keys;
// callers must ensure the properties map is not shared.
//
// Options entries are always removed from properties (these sentinel keys must
// never appear in v1 backend properties) and type-coerced to match the
// backend's strict serde schema. If coercion fails the option key is omitted
// so the backend applies its default. logger may be nil (tests).
func buildV1Event(e apiEvent, logger Logger) eventPayload {
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
					logger.Debugf("v1 options: dropping %s (uncoercible %T value), backend will apply default", m.propKey, v)
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
		Timestamp:  e.timestamp,
		SessionId:  sessionId,
		WindowId:   windowId,
		Options:    options,
		Properties: props,
	}
}

// baseV1Props returns the common properties shared by all v1 event types.
func baseV1Props(isServer bool, disableGeoIP bool) Properties {
	props := Properties{}
	if isServer {
		props.Set("$is_server", true)
	}
	if disableGeoIP {
		props.Set(propertyGeoipDisable, true)
	}
	return props
}

// prepareForSendV1 is the v1 sibling of prepareForSend: it builds the callback
// APIMessage (unchanged legacy shape), serializes the v1 wire event, and returns
// the event uuid for result correlation. logger may be nil (tests).
func prepareForSendV1(msg Message, logger Logger) (json.RawMessage, APIMessage, string, error) {
	apiMsg := msg.APIfy()
	ev := buildV1Event(msg.apifyEvent(), logger)
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
	var myProperties Properties
	if msg.minimalFlagCalledEvent {
		myProperties = baseV1Props(msg.IsServer, false).
			Merge(minimalFlagCalledEventProperties(msg.Properties))
	} else {
		myProperties = baseV1Props(msg.IsServer, false).
			Merge(msg.Properties).
			mergeDefaults(getSystemContext().ToProperties())
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
	myProperties := baseV1Props(msg.IsServer, msg.DisableGeoIP).
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

// apifyEvent builds the v1 intermediate event for a GroupIdentify. The group
// identifiers and $group_set stay in properties (the ingestion groups step reads
// them from there).
func (msg GroupIdentify) apifyEvent() apiEvent {
	myProperties := baseV1Props(msg.IsServer, msg.DisableGeoIP).
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

// apifyEvent builds the v1 intermediate event for an Alias. The canonical
// distinct_id is the top-level field (v1 requirement); the alias merge reads the
// "alias" property and the top-level distinct_id, so no distinct_id is duplicated
// into properties.
func (msg Alias) apifyEvent() apiEvent {
	myProperties := baseV1Props(msg.IsServer, msg.DisableGeoIP).
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

// apifyEvent builds the v1 intermediate event for an Exception. The typed
// exception fields win over custom properties on collision (matching the legacy
// ExceptionInApiProperties marshal precedence).
func (msg Exception) apifyEvent() apiEvent {
	myProperties := baseV1Props(msg.IsServer, msg.DisableGeoIP).
		Merge(msg.Properties).
		mergeDefaults(getSystemContext().ToProperties()).
		Set("$exception_list", msg.ExceptionList)

	if msg.ExceptionFingerprint != nil {
		myProperties.Set("$exception_fingerprint", msg.ExceptionFingerprint)
	}

	return apiEvent{
		event:      "$exception",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
	}
}
