package posthog

import (
	"sync/atomic"
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
}

// analyticsLane reproduces the behavior the single-lane client had.
func analyticsLaneConfig(c Config) laneConfig {
	return laneConfig{
		name:          "analytics",
		path:          capturePath,
		compression:   c.Compression,
		maxEventBytes: c.MaxEventBytes,
		maxBatchBytes: c.MaxBatchBytes,
		maxQueueSize:  c.MaxQueueSize,
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
}

func newLane(cfg laneConfig, batchQueueSize int) *lane {
	return &lane{
		cfg:      cfg,
		msgs:     make(chan preparedMessage, cfg.maxQueueSize),
		batches:  make(chan preparedBatch, batchQueueSize),
		shutdown: make(chan struct{}),
	}
}

// url is the absolute endpoint this lane posts to.
func (l *lane) url(endpoint string) string { return endpoint + l.cfg.path }
