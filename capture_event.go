package posthog

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

// capturePath is the analytics capture endpoint. The "v1" is the backend's
// wire-protocol version, not an internal marker.
const capturePath = "/i/v1/analytics/events"

// Legacy event-property keys moved out of properties into the wire shape.
// propertyProcessPersonProfile, propertySessionID and propertyWindowID are
// defined in request_context.go; reuse them here.
const (
	propertyCookielessMode = "$cookieless_mode"
	propertyIgnoreSentAt   = "$ignore_sent_at"
	propertyProductTourId  = "$product_tour_id"

	optionProcessPersonProfile = "process_person_profile"
)

// Per-event result codes (the only four the backend emits, see
// rust/capture/src/v1/analytics/types.rs EventResult).
const (
	resultOk      = "ok"
	resultWarning = "warning"
	resultDrop    = "drop"
	resultRetry   = "retry"
)

// detailsDuplicateEventUUID is the Details the SDK reports for a copy of a
// repeated uuid that capture dropped.
const detailsDuplicateEventUUID = "duplicate_event_uuid"

// legacyOptionProperties pairs each legacy property with the option it fills.
// Order mirrors posthog-rs.
var legacyOptionProperties = []struct {
	propKey   string
	optionKey string
}{
	{propertyCookielessMode, "cookieless_mode"},
	{propertyIgnoreSentAt, "disable_skew_correction"},
	{propertyProductTourId, "product_tour_id"},
	{propertyProcessPersonProfile, optionProcessPersonProfile},
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
	Timestamp  wireTime               `json:"timestamp"`
	SessionId  *string                `json:"session_id,omitempty"`
	WindowId   *string                `json:"window_id,omitempty"`
	Options    map[string]interface{} `json:"options"`
	Properties Properties             `json:"properties"`
}

// Wire timestamp layouts. Capture requires at least millisecond digits.
const (
	wireTimestampMillis = "2006-01-02T15:04:05.000Z07:00"
	wireTimestampMicros = "2006-01-02T15:04:05.000000Z07:00"
	wireTimestampNanos  = "2006-01-02T15:04:05.000000000Z07:00"
)

// appendWireTimestamp appends t in UTC as RFC 3339 with 3, 6 or 9 fractional
// digits: the fewest that keep t's precision, never fewer than 3.
func appendWireTimestamp(b []byte, t time.Time) []byte {
	t = t.UTC()
	layout := wireTimestampNanos
	switch ns := t.Nanosecond(); {
	case ns%int(time.Millisecond) == 0:
		layout = wireTimestampMillis
	case ns%int(time.Microsecond) == 0:
		layout = wireTimestampMicros
	}
	return t.AppendFormat(b, layout)
}

// formatWireTimestamp formats the batch created_at and the
// PostHog-Request-Timestamp header like an event timestamp.
func formatWireTimestamp(t time.Time) string {
	return string(appendWireTimestamp(nil, t))
}

// wireTime is an event timestamp in the wire format of appendWireTimestamp.
type wireTime time.Time

// MarshalJSON fails, as time.Time does, for a year RFC 3339 cannot hold, so the
// event fails locally instead of reaching capture.
func (t wireTime) MarshalJSON() ([]byte, error) {
	if year := time.Time(t).UTC().Year(); year < 0 || year > 9999 {
		return nil, errors.New("timestamp year outside of range [0,9999]")
	}
	b := make([]byte, 0, len(wireTimestampNanos)+2)
	b = append(b, '"')
	b = appendWireTimestamp(b, time.Time(t))
	return append(b, '"'), nil
}

// captureResponse is the 200 body: a per-uuid map of outcomes.
type captureResponse struct {
	Results map[string]eventResult `json:"results"`
}

// eventResult is a single per-event outcome. Details is optional.
// DuplicatesDropped, set only for a repeated uuid, counts the later copies capture dropped.
type eventResult struct {
	Result            string  `json:"result"`
	Details           *string `json:"details,omitempty"`
	DuplicatesDropped int     `json:"duplicates_dropped,omitempty"`
}

// captureErrorResponse is the best-effort body parsed from a non-2xx response.
type captureErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorUri         string `json:"error_uri"`
}

// apiEvent is the intermediate, pre-options-merge view of a message. Each
// Message produces one via apifyEvent; buildEvent turns it into the wire shape.
type apiEvent struct {
	event      string
	uuid       string
	distinctId string
	timestamp  time.Time
	properties Properties
	options    Options
}

// buildEvent moves legacy properties into options, lifts $session_id and
// $window_id into top-level fields, and returns the wire payload. It mutates
// e.properties by deleting the moved keys; callers must ensure the properties
// map is not shared. e.options is copied, never mutated.
func buildEvent(e apiEvent, logger Logger) eventPayload {
	props := e.properties
	if props == nil {
		props = Properties{}
	}
	options := mergeOptions(e.options, props)
	sessionId := liftStringProperty(props, propertySessionID, logger)
	windowId := liftStringProperty(props, propertyWindowID, logger)
	return eventPayload{
		Event:      e.event,
		Uuid:       e.uuid,
		DistinctId: e.distinctId,
		Timestamp:  wireTime(e.timestamp),
		SessionId:  sessionId,
		WindowId:   windowId,
		Options:    options,
		Properties: props,
	}
}

// mergeOptions returns the caller's options with each legacy property moved
// in. Values are sent unchanged because PostHog validates them. A legacy
// property is always removed from props and fills its option only when that
// option is missing or nil. An option set by neither stays absent.
func mergeOptions(callerOptions Options, props Properties) map[string]interface{} {
	options := make(map[string]interface{}, len(callerOptions)+len(legacyOptionProperties))
	for key, value := range callerOptions {
		options[key] = value
	}
	for _, pair := range legacyOptionProperties {
		legacy, ok := props[pair.propKey]
		if !ok {
			continue
		}
		delete(props, pair.propKey)
		if current, set := options[pair.optionKey]; !set || isNilValue(current) {
			options[pair.optionKey] = legacy
		}
	}
	return options
}

// isNilValue reports whether v is nil or a nil pointer, map, slice or
// interface, all of which serialize as JSON null.
func isNilValue(v interface{}) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// liftStringProperty removes key from props and returns its value when the
// value's JSON form is a string, such as a uuid.UUID or a named string type.
// Capture rejects the whole request when the field is not a string, so any
// other value is dropped with a warning that names its type, never the value.
// A nil value counts as unset and drops silently.
func liftStringProperty(props Properties, key string, logger Logger) *string {
	v, ok := props[key]
	if !ok {
		return nil
	}
	delete(props, key)
	if s, ok := v.(string); ok {
		return &s
	}
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		warnDroppedProperty(logger, key, fmt.Sprintf("%T", v))
		return nil
	}
	// Unmarshaling "null" into a string succeeds and yields "", so check the
	// JSON type first.
	if data[0] == '"' {
		var s string
		if json.Unmarshal(data, &s) == nil {
			return &s
		}
	}
	valueType := jsonTypeName(data)
	if valueType != "null" {
		warnDroppedProperty(logger, key, valueType)
	}
	return nil
}

// jsonTypeName names the JSON type of an encoded value.
func jsonTypeName(data []byte) string {
	switch data[0] {
	case 'n':
		return "null"
	case 't', 'f':
		return "bool"
	case '{':
		return "object"
	case '[':
		return "array"
	default:
		return "number"
	}
}

func warnDroppedProperty(logger Logger, key, valueType string) {
	if logger != nil {
		logger.Warnf("dropping %s: a %s value is not a string", key, valueType)
	}
}

// fillSDKProperties fills the values the SDK adds to every event: system
// context, $lib, $lib_version, $is_server and $geoip_disable. They fill last,
// after the context and default values, so any value the caller set wins.
// $lib and $lib_version follow fillSDKLib. Enqueue calls it before BeforeSend,
// so the hook sees these values and can change or remove them.
func fillSDKProperties(props Properties, isServer, disableGeoIP bool) Properties {
	enrichment := Properties{}
	if isServer {
		enrichment[propertyIsServer] = true
	}
	if disableGeoIP {
		enrichment[propertyGeoipDisable] = true
	}
	// The identity layer also makes filled a new map, so fillSDKLib never
	// writes the caller's.
	filled := fillProperties(props, getSystemContext().ToProperties(), sdkIdentityProperties(), enrichment)
	fillSDKLib(filled)
	return filled
}

var (
	cachedSDKIdentityProps     Properties
	cachedSDKIdentityPropsOnce sync.Once
)

// sdkIdentityProperties returns the SDK's $lib and $lib_version. It is built on
// first use, not at package init, because this package's tests set the
// version in an init function. The map is shared, so never write to it.
func sdkIdentityProperties() Properties {
	cachedSDKIdentityPropsOnce.Do(func() {
		cachedSDKIdentityProps = Properties{
			propertyLib:        SDKName,
			propertyLibVersion: getVersion(),
		}
	})
	return cachedSDKIdentityProps
}

// libString returns v as a string and reports whether it sets $lib or
// $lib_version: only a non-empty string does, including a named string type.
// A missing, nil, empty or non-string value counts as unset.
func libString(v interface{}) (string, bool) {
	if s, ok := v.(string); ok {
		return s, s != ""
	}
	if v == nil {
		return "", false
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.String {
		return rv.String(), rv.Len() > 0
	}
	return "", false
}

// libIdentity returns the $lib and $lib_version PostHog stores for an event
// sent with props. When $lib is unset PostHog takes both from the
// PostHog-Sdk-Info header, which names this SDK, and it adds the header's
// version to this SDK's $lib when the event has none. Another library's pair is
// stored as sent, so its version is "" when it is unset. Callback messages use
// it to report the identity the event is stored with.
func libIdentity(props Properties) (lib, libVersion string) {
	lib, set := libString(props[propertyLib])
	if !set {
		return SDKName, getVersion()
	}
	libVersion, set = libString(props[propertyLibVersion])
	if !set && lib == SDKName {
		return lib, getVersion()
	}
	return lib, libVersion
}

// fillSDKLib writes the SDK's own values into props where libIdentity fills
// them: both when $lib is unset, replacing a lone $lib_version, and only the
// version when $lib is this SDK's and the version is unset. A caller's other
// $lib keeps its own $lib_version or none. It returns libIdentity's values.
func fillSDKLib(props Properties) (lib, libVersion string) {
	lib, libVersion = libIdentity(props)
	identity := sdkIdentityProperties()
	if _, set := libString(props[propertyLib]); !set {
		props[propertyLib] = identity[propertyLib]
		props[propertyLibVersion] = identity[propertyLibVersion]
	} else if _, set := libString(props[propertyLibVersion]); !set && lib == SDKName {
		props[propertyLibVersion] = identity[propertyLibVersion]
	}
	return lib, libVersion
}

// boolProperty reads a boolean SDK value back from properties after
// BeforeSend, which may have changed or removed it.
func boolProperty(props Properties, key string) bool {
	value, _ := props[key].(bool)
	return value
}

// prepareForSend builds the callback APIMessage, serializes the wire event,
// and returns the event uuid for per-event result correlation. The uuid is
// canonicalized because BeforeSend may set any form capture accepts, while
// capture keys its results by the canonical form.
func prepareForSend(msg Message, logger Logger) (json.RawMessage, APIMessage, string, error) {
	apiMsg := msg.APIfy()
	ev := buildEvent(msg.apifyEvent(), logger)
	data, err := marshalEventPayload(ev)
	if err != nil {
		return nil, apiMsg, canonicalUUID(ev.Uuid), err
	}
	return json.RawMessage(data), apiMsg, canonicalUUID(ev.Uuid), nil
}

// apifyEvent builds the intermediate event for a Capture.
func (msg Capture) apifyEvent() apiEvent {
	myProperties := Properties{}.Merge(msg.Properties)

	if msg.Groups != nil {
		myProperties.Set("$groups", mergeOverNested(myProperties["$groups"], msg.Groups))
	}

	return apiEvent{
		event:      msg.Event,
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
		options:    msg.Options,
	}
}

// apifyEvent builds the intermediate event for an Identify. The person
// properties are folded into properties.$set (there is no top-level $set) and
// win key by key over a $set in EventProperties.
func (msg Identify) apifyEvent() apiEvent {
	myProperties := Properties{}.Merge(msg.EventProperties)

	if msg.Properties != nil {
		myProperties.Set("$set", mergeOverNested(myProperties["$set"], msg.Properties))
	}

	return apiEvent{
		event:      "$identify",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
		options:    msg.Options,
	}
}

// apifyEvent builds the intermediate event for a GroupIdentify. The group
// identifiers and $group_set stay in properties (the ingestion groups step reads
// them from there). Properties win key by key over a $group_set in
// EventProperties.
func (msg GroupIdentify) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(msg.EventProperties).
		Set("$group_type", msg.Type).
		Set("$group_key", msg.Key)

	if msg.Properties != nil {
		myProperties.Set("$group_set", mergeOverNested(myProperties["$group_set"], msg.Properties))
	}

	distinctID := msg.DistinctId
	if distinctID == "" {
		distinctID = groupDistinctID(msg.Type, msg.Key)
	}

	return apiEvent{
		event:      "$groupidentify",
		uuid:       msg.Uuid,
		distinctId: distinctID,
		timestamp:  msg.Timestamp,
		properties: myProperties,
		options:    msg.Options,
	}
}

// apifyEvent builds the intermediate event for an Alias. The canonical
// distinct_id is the top-level field; the alias merge reads the
// "alias" property and the top-level distinct_id, so no distinct_id is duplicated
// into properties.
func (msg Alias) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(msg.EventProperties).
		Set("alias", msg.Alias)

	return apiEvent{
		event:      "$create_alias",
		uuid:       msg.Uuid,
		distinctId: msg.DistinctId,
		timestamp:  msg.Timestamp,
		properties: myProperties,
		options:    msg.Options,
	}
}

// apifyEvent builds the intermediate event for an Exception. The typed
// exception fields win over custom properties on collision (matching the legacy
// ExceptionInApiProperties marshal precedence).
func (msg Exception) apifyEvent() apiEvent {
	myProperties := Properties{}.
		Merge(msg.Properties).
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
		options:    msg.Options,
	}
}
