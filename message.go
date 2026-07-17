package posthog

import (
	"time"

	json "github.com/goccy/go-json"
	"github.com/google/uuid"
)

// Callback is implemented by applications that want delivery notifications.
//
// Callback methods are called by a client's internal goroutines. There are no
// guarantees about which goroutine triggers the callbacks, whether calls are
// made sequentially or in parallel, or whether callback order matches enqueue order.
//
// Callback methods must return quickly and avoid long blocking operations so
// they do not interfere with the client's internal workflow.
type Callback interface {

	// Success is called for every message that was successfully sent to the API.
	Success(APIMessage)

	// Failure is called for every message that failed to be sent to the API and
	// will be discarded by the client. The error describes the send or serialization failure.
	Failure(APIMessage, error)
}

// BeforeSendFunc is called before a message is converted to the PostHog API format.
// It can return a modified message, or nil to drop the message. If the hook
// panics, returns an invalid message, or returns a different message type, the
// message is dropped.
//
// The SDK passes an isolated copy of SDK-owned mutable fields where practical:
// Properties and Groups clone common JSON-like maps and slices, and Exception
// clones its exception list, stack traces, mechanisms, fingerprint, and debug
// images. Arbitrary
// reference values stored inside Properties or Groups (for example, pointers or
// custom mutable structs held as interface{} values) are not deep-cloned and can
// still share state with the caller.
type BeforeSendFunc func(Message) Message

// Message represents a PostHog object that can be queued with Client.Enqueue.
//
// Built-in message types such as Capture, Identify, Alias, GroupIdentify, and
// Exception implement this interface. The unexported internal method prevents
// external packages from defining arbitrary message implementations.
type Message interface {

	// Validate checks the internal structure of the message. It returns nil when
	// the message is valid or an error describing the invalid field.
	Validate() error
	// APIfy converts the message into its PostHog batch API representation.
	APIfy() APIMessage

	// apifyEvent converts the message into the capture-v1 intermediate event.
	// Unexported so it does not widen the public surface. The returned properties
	// are caller-owned; buildV1Event may mutate them in place.
	apifyEvent() apiEvent

	// internal prevents external packages from satisfying Message. Calling it panics.
	internal()
}

// Returns the time value passed as first argument, unless it's the zero-value,
// in that case the default value passed as second argument is returned.
func makeTimestamp(t time.Time, def time.Time) time.Time {
	if t == (time.Time{}) {
		return def
	}
	return t
}

// makeUUID returns the UUID passed as first argument if non-empty and valid,
// otherwise generates and returns a new random UUID (v4).
func makeUUID(u string) string {
	if u != "" && uuid.Validate(u) == nil {
		return u
	}
	return uuid.New().String()
}

// batch represents objects sent to the /batch/ endpoint with pre-serialized messages.
// Messages are pre-serialized as json.RawMessage for efficient batch building -
// json.Marshal embeds them directly without re-encoding.
type batch struct {
	ApiKey              string            `json:"api_key"`
	HistoricalMigration bool              `json:"historical_migration,omitempty"`
	Messages            []json.RawMessage `json:"batch"`
}

// APIMessage is a wire-format message produced by Message.APIfy and passed to callbacks.
// Legacy API message structs may still expose top-level fields such as type,
// library, library_version, and send_feature_flags for compatibility. Capture
// ingestion uses event plus properties such as $lib, $lib_version,
// $feature/<key>, and $active_feature_flags instead.
type APIMessage interface{}

// prepareForSend creates the API message and serializes it to JSON.
// Returns pre-serialized JSON for efficient batch building, the original
// APIMessage for callbacks, and any serialization error.
// Size is derived from len(json.RawMessage) when needed - O(1) operation.
func prepareForSend(msg Message) (json.RawMessage, APIMessage, error) {
	apiMsg := msg.APIfy()
	data, err := json.Marshal(apiMsg)
	if err != nil {
		return nil, apiMsg, err
	}
	return json.RawMessage(data), apiMsg, nil
}

const (
	maxBatchBytes   = 500000
	maxMessageBytes = 500000
)
