package posthog

import (
	"net/http"
	"sync/atomic"
	"time"
)

// laneConfig is everything that differs between capture lanes. One pipeline
// serves both; a lane is chosen by the public method the caller used, never by
// inspecting the event.
type laneConfig struct {
	// name identifies the lane in logs.
	name string
	// path is the capture endpoint, joined to Config.Endpoint.
	path string
	// compression is the codec for this lane's request bodies.
	compression CompressionMode
	// maxEventBytes rejects a single serialized event larger than this.
	maxEventBytes int
	// maxBatchBytes closes a batch before appending an event that would
	// exceed it.
	maxBatchBytes int
	// maxQueueSize bounds the lane's in-memory message queue.
	maxQueueSize int
	// uploadTimeout bounds one upload from this lane.
	uploadTimeout time.Duration
}

const (
	// aiCapturePath is the dedicated AI capture endpoint. Its "v1" is the
	// backend's wire-protocol version, not an internal marker.
	aiCapturePath = "/i/v1/ai/events"

	// aiMaxEventBytes is the endpoint's per-event ceiling, applied there to the
	// serialized properties alone.
	aiMaxEventBytes = 8 << 20

	// aiEnvelopeHeadroom is added to the local guard because it measures the
	// whole serialized event, not just properties. Without it an event whose
	// properties sit at the ceiling is refused here even though the endpoint
	// would accept it. The guard stays coarse on purpose: it exists to skip a
	// doomed multi-megabyte upload, not to reproduce the endpoint's check.
	aiEnvelopeHeadroom = 64 << 10

	// aiBatchBytesTarget closes an AI batch before appending an event that
	// would exceed it. Well under the endpoint's 20 MiB compressed body limit,
	// and the same target posthog-python and posthog-node use.
	aiBatchBytesTarget = 5 << 20
)

// analyticsLane reproduces the behavior the single-lane client had.
func analyticsLaneConfig(c Config) laneConfig {
	return laneConfig{
		name:          "analytics",
		path:          capturePath,
		compression:   c.Compression,
		maxEventBytes: c.MaxEventBytes,
		maxBatchBytes: c.MaxBatchBytes,
		maxQueueSize:  c.MaxQueueSize,
		uploadTimeout: c.BatchUploadTimeout,
	}
}

// aiLaneConfig is the AI lane. Its size limits are constants rather than
// Config fields, matching posthog-rs, posthog-python and posthog-node: they
// track the backend's limits rather than caller preference.
func aiLaneConfig(c Config) laneConfig {
	return laneConfig{
		name:          "capture-ai",
		path:          aiCapturePath,
		compression:   c.CaptureAICompression,
		maxEventBytes: aiMaxEventBytes + aiEnvelopeHeadroom,
		maxBatchBytes: aiBatchBytesTarget,
		maxQueueSize:  c.CaptureAIMaxQueueSize,
		uploadTimeout: c.CaptureAIBatchUploadTimeout,
	}
}

// lane owns one capture pipeline: its own message queue, batch queue, in-flight
// accounting and shutdown signal. Lanes share the client's HTTP client, retry
// policy and callbacks.
type lane struct {
	cfg laneConfig

	// msgs carries prepared messages from Enqueue to the lane's loop.
	msgs chan preparedMessage
	// batches carries assembled batches to the upload workers. It doubles as a
	// concurrency limiter: when full, new batches are shed.
	batches chan preparedBatch
	// inFlight counts batches handed to workers but not yet finished.
	inFlight atomic.Int64
	// shutdown is closed by the lane's loop once it has drained.
	shutdown chan struct{}
	// http carries this lane's upload timeout. Lanes share one Transport, so
	// they share the connection pool.
	http *http.Client
}

func newLane(cfg laneConfig, batchQueueSize int, transport http.RoundTripper) *lane {
	return &lane{
		cfg:      cfg,
		msgs:     make(chan preparedMessage, cfg.maxQueueSize),
		batches:  make(chan preparedBatch, batchQueueSize),
		shutdown: make(chan struct{}),
		http:     &http.Client{Transport: transport, Timeout: cfg.uploadTimeout},
	}
}

// url is the absolute endpoint this lane posts to.
func (l *lane) url(endpoint string) string { return endpoint + l.cfg.path }

// aiLane returns the AI lane, starting it on first use, or nil once the client
// is closed.
//
// Ordering matters here. Close marks the client closed *before* it reads this
// lane, so a caller that observes "not closed" either started the lane in time
// for Close to see and drain it, or observes "closed" on the re-check below and
// declines to use it. Without the re-check a lane could be started after Close
// had already looked, and nothing would wait for it.
func (c *client) aiLane() *lane {
	if c.closed.Load() {
		return nil
	}
	c.aiOnce.Do(func() {
		l := newLane(aiLaneConfig(c.Config), c.MaxEnqueuedRequests, c.http.Transport)
		c.ai.Store(l)
		go c.loop(l)
	})
	if c.closed.Load() {
		// Close ran while we were starting. Its quit signal is already closed,
		// so the lane drains and exits on its own; we just must not hand it a
		// message nobody is waiting for.
		return nil
	}
	return c.ai.Load()
}
