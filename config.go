package posthog

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CompressionMode specifies the compression algorithm for batch payloads.
type CompressionMode uint8

const (
	// CompressionNone disables compression (default).
	CompressionNone CompressionMode = 0
	// CompressionGzip enables GZIP compression for batch payloads. Valid on
	// both capture modes.
	CompressionGzip CompressionMode = 1
	// CompressionZstd enables Zstandard compression. Requires
	// CaptureModeAnalyticsV1 (the legacy /batch/ endpoint cannot decode it).
	CompressionZstd CompressionMode = 2
	// CompressionDeflate enables zlib (RFC 1950) compression, sent as
	// Content-Encoding: deflate. Requires CaptureModeAnalyticsV1.
	CompressionDeflate CompressionMode = 3
	// CompressionBrotli enables Brotli compression, sent as
	// Content-Encoding: br. Requires CaptureModeAnalyticsV1.
	CompressionBrotli CompressionMode = 4
)

// String returns a human-readable codec name, used in config validation
// errors and debug logs. It is not the on-the-wire Content-Encoding token
// (brotli's token is "br"); see compressV1Body for wire tokens.
func (m CompressionMode) String() string {
	switch m {
	case CompressionNone:
		return "none"
	case CompressionGzip:
		return "gzip"
	case CompressionZstd:
		return "zstd"
	case CompressionDeflate:
		return "deflate"
	case CompressionBrotli:
		return "brotli"
	default:
		return fmt.Sprintf("CompressionMode(%d)", uint8(m))
	}
}

// CaptureMode selects the capture wire protocol used for event ingestion.
type CaptureMode uint8

const (
	// CaptureModeLegacy sends events to the legacy POST /batch/ endpoint. This
	// is the default, so upgrading is transparent to existing callers.
	CaptureModeLegacy CaptureMode = 0
	// CaptureModeAnalyticsV1 opts into POST /i/v1/analytics/events (Bearer auth,
	// per-event results, partial retry).
	CaptureModeAnalyticsV1 CaptureMode = 1
)

// Config carries configuration options used when constructing a Client with NewWithConfig.
//
// Each exported field's zero value is either meaningful or replaced by the
// default documented on that field.
type Config struct {

	// Endpoint is the PostHog API host used for event ingestion and feature flag requests.
	// If empty, it defaults to DefaultEndpoint.
	Endpoint string

	// SecretKey is the credential used for local feature flag evaluation, remote
	// config payloads, and lower-latency flag APIs. It accepts either a Personal
	// API Key (phx_...) or a Project Secret API Key (phs_...). If empty, feature
	// flag methods fall back to the /flags endpoint unless the method requires
	// local-only behavior. When both SecretKey and PersonalApiKey are set,
	// SecretKey takes precedence.
	// See https://posthog.com/docs/api/overview for how to create these keys.
	SecretKey string

	// PersonalApiKey is a deprecated alias for SecretKey. It is still honored for
	// backwards compatibility but SecretKey is preferred.
	//
	// Deprecated: use SecretKey instead.
	PersonalApiKey string

	// DisableGeoIP controls whether event and feature flag requests include
	// $geoip_disable/geoip_disable. Nil defaults to true because this SDK usually
	// runs server-side; set Ptr(false) to allow GeoIP lookup.
	DisableGeoIP *bool

	// IsServer controls whether events include the $is_server property.
	//
	// Defaults to true: nil is treated as true because posthog-go usually runs
	// server-side, and $is_server lets PostHog identify server-side events.
	//
	// Set Ptr(false) when using posthog-go as a CLI or client so the event is
	// not flagged as server-side and the device OS is attributed normally. When
	// resolved to false, $is_server is omitted from every event entirely.
	//
	//	// CLI/client usage
	//	config := posthog.Config{IsServer: posthog.Ptr(false)}
	IsServer *bool

	// Interval is the flush interval for queued messages. Messages are sent when
	// BatchSize is reached or when this timer fires. If zero, it defaults to
	// DefaultInterval.
	Interval time.Duration

	// DefaultFeatureFlagsPollingInterval is the interval for reloading local feature
	// flag definitions when SecretKey is configured. If zero, it defaults to
	// DefaultFeatureFlagsPollingInterval.
	DefaultFeatureFlagsPollingInterval time.Duration

	// FeatureFlagRequestTimeout is the timeout for feature flag and remote config
	// HTTP requests. If zero, it defaults to DefaultFeatureFlagRequestTimeout.
	// Use time.Duration values such as 3 * time.Second.
	FeatureFlagRequestTimeout time.Duration

	// FeatureFlagRequestMaxRetries is the maximum number of retries after a transient
	// network error on /flags requests. If nil, it defaults to 1. Set to 0 to disable.
	FeatureFlagRequestMaxRetries *int

	// NextFeatureFlagsPollingTick optionally calculates the next local feature flag
	// polling delay. When set, it overrides DefaultFeatureFlagsPollingInterval.
	NextFeatureFlagsPollingTick func() time.Duration

	// HistoricalMigration marks captured batches as historical migration traffic.
	// See https://posthog.com/docs/migrate for migration guidance.
	HistoricalMigration bool

	// Transport is the HTTP transport used by the client. Set it to customize
	// low-level request behavior such as connection pooling or proxies. If nil,
	// the client uses a clone of http.DefaultTransport with SDK defaults.
	Transport http.RoundTripper

	// Logger receives informational, warning, and error messages from background
	// operations. If nil, the client logs to os.Stderr with the standard logger.
	Logger Logger

	// DefaultEventProperties are merged into every Capture event before sending.
	// They are useful for common metadata like service name or app version. On key
	// conflicts, values from DefaultEventProperties overwrite event properties.
	DefaultEventProperties Properties

	// Callback receives success or failure notifications for messages sent to the
	// PostHog batch API.
	Callback Callback

	// BeforeSend is called after SDK enrichment and before messages are serialized.
	// Return the message to send a modified version, or nil to drop it.
	BeforeSend BeforeSendFunc

	// BatchSize is the maximum number of messages sent in one batch API call.
	// Messages are sent when BatchSize is reached or when Interval fires. If zero,
	// it defaults to DefaultBatchSize. The API still enforces a 500KB request limit.
	BatchSize int

	// MaxQueueSize is the maximum number of messages buffered in memory waiting to
	// be batched and sent. If zero, it defaults to DefaultMaxQueueSize. It is
	// clamped up to BatchSize so the queue can always hold at least one full batch.
	//
	// When the queue is full, Enqueue drops the newest message rather than blocking
	// the caller and returns ErrQueueFull (the drop is not reported via
	// Callback.Failure). Bulk or backfill workloads that enqueue faster than the
	// client can upload should check the error returned by Enqueue for ErrQueueFull
	// and throttle or retry (or raise MaxQueueSize), otherwise events are dropped,
	// not delayed.
	MaxQueueSize int

	// Verbose enables more frequent and detailed debug logging through Logger.
	Verbose bool

	// RetryAfter returns the delay before retrying a failed batch upload. The int
	// argument is the retry attempt number. If nil, DefaultBackoff().Duration is used.
	RetryAfter func(int) time.Duration

	// MaxRetries is the maximum number of retries after the first send attempt.
	// It must be in [0,9]. If nil, it defaults to 3 retries (4 total attempts).
	MaxRetries *int

	// ShutdownTimeout is the maximum time Close waits for in-flight messages to be
	// sent. If zero or negative, Close waits indefinitely for backward compatibility.
	ShutdownTimeout time.Duration

	// BatchUploadTimeout is the timeout for uploading one batch to the /batch/
	// endpoint. If zero, it defaults to DefaultBatchUploadTimeout.
	BatchUploadTimeout time.Duration

	// BatchSubmitTimeout is the maximum time to wait when submitting a batch to the
	// worker pool while its queue is full. If zero, it defaults to
	// DefaultBatchSubmitTimeout. Set a negative duration for non-blocking behavior
	// that drops immediately when the queue is full.
	BatchSubmitTimeout time.Duration

	// MaxEnqueuedRequests is the maximum number of batches waiting for upload.
	// When the queue is full, new batches are dropped and the failure callback is
	// invoked. If zero, it defaults to DefaultMaxEnqueuedRequests.
	MaxEnqueuedRequests int

	// Compression selects the compression mode for batch payloads. CompressionGzip
	// compresses payloads and adds the appropriate headers/query params. If zero,
	// it defaults to CompressionNone.
	Compression CompressionMode

	// CaptureMode selects the capture wire protocol. It defaults to
	// CaptureModeLegacy (POST /batch/). Set CaptureModeAnalyticsV1 to opt into
	// POST /i/v1/analytics/events.
	CaptureMode CaptureMode

	// A function called by the client to get the current time, `time.Now` is
	// used by default.
	// This field is not exported and only exposed internally to control concurrency.
	now func() time.Time

	// maxAttempts is a maximum numbers we try to send data to capture endpoint, must be in range [1,10].
	maxAttempts int
}

// GetDisableGeoIP instructs the client to set $geoip_disable on event properties or feature flag requests.
// It is on by default as Go is mainly used on server side and to be compatible with posthog-python.
func (c Config) GetDisableGeoIP() bool {
	return c.DisableGeoIP == nil || *c.DisableGeoIP
}

// GetIsServer reports whether events should include the $is_server property.
// It is on by default because posthog-go is mainly used server-side; set
// Config.IsServer to Ptr(false) for CLI/client usage to omit $is_server.
func (c Config) GetIsServer() bool {
	return c.IsServer == nil || *c.IsServer
}

const (
	// SDKName is the library identifier sent in event metadata.
	SDKName = "posthog-go"

	// DefaultEndpoint is the default PostHog API host used when Config.Endpoint is empty.
	DefaultEndpoint = "https://us.i.posthog.com"

	// DefaultInterval is the default flush interval used when Config.Interval is zero.
	DefaultInterval = 5 * time.Second

	// DefaultFeatureFlagsPollingInterval is the default local feature flag reload interval.
	DefaultFeatureFlagsPollingInterval = 5 * time.Minute

	// DefaultFeatureFlagRequestTimeout is the default timeout for feature flag requests.
	DefaultFeatureFlagRequestTimeout = 3 * time.Second

	// DefaultBatchSize is the default batch size used when Config.BatchSize is zero.
	DefaultBatchSize = 100

	// DefaultMaxAttempts is the total number of capture delivery attempts (1
	// initial + retries) used when Config.MaxRetries is unset or out of range.
	// Chosen to match the cross-SDK Capture V1 parity standard (posthog-rs
	// defaults to the same envelope). Applies to both the v0 and v1 send paths.
	DefaultMaxAttempts = 4

	// DefaultBatchUploadTimeout is the default timeout for uploading batched
	// events to the /batch/ endpoint.
	DefaultBatchUploadTimeout = 10 * time.Second

	// DefaultBatchSubmitTimeout is the default timeout for submitting batches
	// to the worker pool when the queue is full. This allows workers time to
	// complete during transient latency spikes, reducing unnecessary data loss.
	DefaultBatchSubmitTimeout = 100 * time.Millisecond

	// DefaultMaxEnqueuedRequests is the default maximum number of batches that
	// can be queued for sending.
	DefaultMaxEnqueuedRequests = 1000

	// DefaultMaxQueueSize is the default in-memory message queue capacity used when
	// Config.MaxQueueSize is zero. It matches the posthog-python, posthog-rs, and
	// posthog-node defaults so backend SDKs behave consistently under bursty load.
	DefaultMaxQueueSize = 10000
)

func (c *Config) normalize() {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.PersonalApiKey = strings.TrimSpace(c.PersonalApiKey)
}

// effectiveSecretKey prefers SecretKey and falls back to the deprecated PersonalApiKey.
func (c *Config) effectiveSecretKey() string {
	if c.SecretKey != "" {
		return c.SecretKey
	}
	return c.PersonalApiKey
}

// Validate verifies that fields that don't have zero-values are set to valid values,
// returns an error describing the problem if a field was invalid.
func (c *Config) Validate() error {
	c.normalize()

	if c.Interval < 0 {
		return ConfigError{
			Reason: "negative time intervals are not supported",
			Field:  "Interval",
			Value:  c.Interval,
		}
	}

	if c.BatchSize < 0 {
		return ConfigError{
			Reason: "negative batch sizes are not supported",
			Field:  "BatchSize",
			Value:  c.BatchSize,
		}
	}

	if c.MaxQueueSize < 0 {
		return ConfigError{
			Reason: "negative queue sizes are not supported",
			Field:  "MaxQueueSize",
			Value:  c.MaxQueueSize,
		}
	}

	if _, err := url.Parse(c.Endpoint); err != nil {
		return ConfigError{
			Reason: "invalid endpoint",
			Field:  "Endpoint",
			Value:  c.Endpoint,
		}
	}

	if c.MaxRetries != nil && (*c.MaxRetries < 0 || *c.MaxRetries > 9) {
		return ConfigError{
			Reason: "max retries out of range [0,9]",
			Field:  "MaxRetries",
			Value:  *c.MaxRetries,
		}
	}

	if c.FeatureFlagRequestMaxRetries != nil && (*c.FeatureFlagRequestMaxRetries < 0 || *c.FeatureFlagRequestMaxRetries > 9) {
		return ConfigError{
			Reason: "feature flag request max retries out of range [0,9]",
			Field:  "FeatureFlagRequestMaxRetries",
			Value:  *c.FeatureFlagRequestMaxRetries,
		}
	}

	switch c.Compression {
	case CompressionNone, CompressionGzip:
		// Valid on both legacy and v1 capture modes.
	case CompressionZstd, CompressionDeflate, CompressionBrotli:
		// Only the v1 endpoint decodes these; the legacy /batch/ endpoint
		// understands gzip/lz64/base64 only, so reject them up front.
		if c.CaptureMode != CaptureModeAnalyticsV1 {
			return ConfigError{
				Reason: c.Compression.String() + " compression requires CaptureModeAnalyticsV1",
				Field:  "Compression",
				Value:  c.Compression,
			}
		}
	default:
		return ConfigError{
			Reason: "invalid compression mode",
			Field:  "Compression",
			Value:  c.Compression,
		}
	}

	if c.CaptureMode > CaptureModeAnalyticsV1 {
		return ConfigError{
			Reason: "invalid capture mode",
			Field:  "CaptureMode",
			Value:  c.CaptureMode,
		}
	}

	return nil
}

// Given a config object as argument the function will set all zero-values to
// their defaults and return the modified object.
func makeConfig(c Config) Config {
	c.normalize()

	if len(c.Endpoint) == 0 {
		c.Endpoint = DefaultEndpoint
	}

	if c.Interval == 0 {
		c.Interval = DefaultInterval
	}

	if c.DefaultFeatureFlagsPollingInterval == 0 {
		c.DefaultFeatureFlagsPollingInterval = DefaultFeatureFlagsPollingInterval
	}

	if c.FeatureFlagRequestTimeout == 0 {
		c.FeatureFlagRequestTimeout = DefaultFeatureFlagRequestTimeout
	}

	// Note: c.Transport == nil is handled by makeHttpClient() which clones
	// DefaultTransport with tuned connection pool settings

	if c.Logger == nil {
		c.Logger = newDefaultLogger(c.Verbose)
	}

	if c.BatchSize == 0 {
		c.BatchSize = DefaultBatchSize
	}

	if c.MaxQueueSize == 0 {
		c.MaxQueueSize = DefaultMaxQueueSize
	}
	// The queue must be able to hold at least one full batch, otherwise a batch
	// could never accumulate before the queue overflows.
	if c.MaxQueueSize < c.BatchSize {
		c.MaxQueueSize = c.BatchSize
	}

	if c.RetryAfter == nil {
		c.RetryAfter = DefaultBackoff().Duration
	}

	if c.now == nil {
		c.now = time.Now
	}

	if c.MaxRetries != nil && 0 <= *c.MaxRetries && *c.MaxRetries <= 9 {
		c.maxAttempts = 1 + *c.MaxRetries
	} else {
		c.maxAttempts = DefaultMaxAttempts
	}

	// Note: ShutdownTimeout == 0 means wait indefinitely (backward compatible).
	// Users opt-in to timeout by setting a positive duration.

	if c.BatchUploadTimeout == 0 {
		c.BatchUploadTimeout = DefaultBatchUploadTimeout
	}

	if c.BatchSubmitTimeout == 0 {
		c.BatchSubmitTimeout = DefaultBatchSubmitTimeout
	}

	if c.MaxEnqueuedRequests == 0 {
		c.MaxEnqueuedRequests = DefaultMaxEnqueuedRequests
	}

	if c.GetDisableGeoIP() {
		if c.DefaultEventProperties == nil {
			c.DefaultEventProperties = NewProperties()
		}
		c.DefaultEventProperties.Set(propertyGeoipDisable, true)
	}

	return c
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T {
	return &v
}
