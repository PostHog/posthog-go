package posthog

import (
	"time"

	json "github.com/goccy/go-json"
)

// Values implementing this interface are used by posthog clients to notify
// the application when a message send succeeded or failed.
//
// Callback methods are called by a client's internal goroutines, there are no
// guarantees on which goroutine will trigger the callbacks, the calls can be
// made sequentially or in parallel, the order doesn't depend on the order of
// messages were queued to the client.
//
// Callback methods must return quickly and not cause long blocking operations
// to avoid interferring with the client's internal work flow.
type Callback interface {

	// This method is called for every message that was successfully sent to
	// the API.
	Success(APIMessage)

	// This method is called for every message that failed to be sent to the
	// API and will be discarded by the client.
	Failure(APIMessage, error)
}

// This interface is used to represent posthog objects that can be sent via
// a client.
//
// Types like posthog.Capture, posthog.Alias, etc... implement this interface
// and therefore can be passed to the posthog.Client.Send method.
type Message interface {

	// Validate validates the internal structure of the message, the method must return
	// nil if the message is valid, or an error describing what went wrong.
	Validate() error
	APIfy() APIMessage

	// internal is an unexposed interface function to ensure only types defined within this package can satisfy the Message interface. Invoking this method will panic.
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

// batch represents objects sent to the /batch/ endpoint with pre-serialized messages.
// Messages are pre-serialized as json.RawMessage for efficient batch building -
// json.Marshal embeds them directly without re-encoding.
type batch struct {
	ApiKey              string            `json:"api_key"`
	HistoricalMigration bool              `json:"historical_migration,omitempty"`
	Messages            []json.RawMessage `json:"batch"`
}

type APIMessage interface{}

const (
	maxBatchBytes   = 500000
	maxMessageBytes = 500000
)
