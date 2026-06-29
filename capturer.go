package posthog

import (
	json "github.com/goccy/go-json"
)

// capturer isolates every legacy-vs-v1 divergence: both the per-message wire
// serialization and the batch send/retry semantics. The client picks one
// implementation once at construction from Config.CaptureMode, so the public
// API and the queue/batching machinery stay protocol-agnostic.
type capturer interface {
	// prepare serializes one message into this mode's wire bytes, returning the
	// raw JSON, the original APIMessage (for callbacks), and the event uuid (used
	// for v1 per-event result correlation; "" for legacy).
	prepare(msg Message) (json.RawMessage, APIMessage, string, error)
	// send delivers a prepared batch using this mode's endpoint, headers, and
	// retry policy.
	send(pb preparedBatch)
}

// legacyCapturer wraps the existing /batch/ serialization and send path.
type legacyCapturer struct{ c *client }

func (l legacyCapturer) prepare(m Message) (json.RawMessage, APIMessage, string, error) {
	data, apiMsg, err := prepareForSend(m)
	return data, apiMsg, "", err
}

func (l legacyCapturer) send(pb preparedBatch) { l.c.send(pb) }

// analyticsV1Capturer wraps the capture-v1 serialization and send path.
type analyticsV1Capturer struct{ c *client }

func (v analyticsV1Capturer) prepare(m Message) (json.RawMessage, APIMessage, string, error) {
	return prepareForSendV1(m, v.c.Logger)
}

func (v analyticsV1Capturer) send(pb preparedBatch) { v.c.sendV1(pb) }
