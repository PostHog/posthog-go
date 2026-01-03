package posthog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"
	"github.com/hashicorp/golang-lru/v2"
)

const (
	unimplementedError = "not implemented"
	CACHE_DEFAULT_SIZE = 300_000

	propertyGeoipDisable = "$geoip_disable"
)

type EnqueueClient interface {
	// Enqueue queues a message to be sent by the client when the conditions for a batch
	// upload are met.
	// This is the main method you'll be using, a typical flow would look like
	// this:
	//
	//	client := posthog.New(apiKey)
	//	...
	//	client.Enqueue(posthog.Capture{ ... })
	//	...
	//	client.Close()
	//
	// The method returns an error if the message queue could not be queued, which
	// happens if the client was already closed at the time the method was
	// called or if the message was malformed.
	Enqueue(Message) error
}

// Client interface is the main API exposed by the posthog package.
// Values that satisfy this interface are returned by the client constructors
// provided by the package and provide a way to send messages via the HTTP API.
type Client interface {
	io.Closer
	EnqueueClient

	// IsFeatureEnabled returns if a feature flag is on for a given user based on their distinct ID
	IsFeatureEnabled(FeatureFlagPayload) (interface{}, error)

	// GetFeatureFlag returns variant value if multivariant flag or otherwise a boolean indicating
	// if the given flag is on or off for the user
	GetFeatureFlag(FeatureFlagPayload) (interface{}, error)

	// GetFeatureFlagPayload returns feature flag's payload value matching key for user (supports multivariate flags).
	GetFeatureFlagPayload(FeatureFlagPayload) (string, error)

	// GetRemoteConfigPayload returns decrypted feature flag payload value for remote config flags.
	GetRemoteConfigPayload(string) (string, error)

	// GetAllFlags returns all flags for a user
	GetAllFlags(FeatureFlagPayloadNoKey) (map[string]interface{}, error)

	// ReloadFeatureFlags forces a reload of feature flags
	// NB: This is only available when using a PersonalApiKey
	ReloadFeatureFlags() error

	// GetFeatureFlags gets all feature flags, for testing only.
	// NB: This is only available when using a PersonalApiKey
	GetFeatureFlags() ([]FeatureFlag, error)

	// CloseWithContext gracefully shuts down the client with the provided context.
	// The context can be used to control the shutdown deadline.
	CloseWithContext(context.Context) error
}

// preparedMessage bundles a pre-serialized message with the original APIMessage.
// The data field contains pre-serialized JSON for efficient batch building.
// The msg field retains the original APIMessage for callbacks.
// Size is obtained via len(data) - O(1) since json.RawMessage is []byte.
type preparedMessage struct {
	data json.RawMessage // pre-serialized JSON for batch submission
	msg  APIMessage      // original message for callbacks
}

// preparedBatch holds both raw data for efficient serialization and
// original API messages for callbacks.
type preparedBatch struct {
	data []json.RawMessage // pre-serialized messages for batch submission
	msgs []APIMessage      // original messages for callbacks
}

type client struct {
	Config
	key string

	// This channel is where the `Enqueue` method writes messages so they can be
	// picked up and pushed by the backend goroutine taking care of applying the
	// batching rules. Messages are pre-converted to APIMessage format with
	// pre-computed size to avoid race conditions.
	msgs chan preparedMessage

	// Channel for sending batches to worker pool
	batches chan preparedBatch

	// Tracks in-flight batches for graceful shutdown
	inFlight atomic.Int64

	// These two channels are used to synchronize the client shutting down when
	// `Close` is called.
	// The first channel is closed to signal the backend goroutine that it has
	// to stop, then the second one is closed by the backend goroutine to signal
	// that it has finished flushing all queued messages.
	quit     chan struct{}
	shutdown chan struct{}

	// Context and cancel function for graceful shutdown.
	// When Close is called, the context is cancelled to signal all goroutines
	// to stop, including in-flight HTTP requests.
	ctx    context.Context
	cancel context.CancelFunc

	// closeOnce ensures Close is idempotent
	closeOnce sync.Once

	// closed is set to true when the client is closed, used to fast-fail Enqueue
	closed atomic.Bool

	// This HTTP client is used to send requests to the backend, it uses the
	// HTTP transport provided in the configuration.
	http http.Client

	// A background poller for fetching feature flags
	featureFlagsPoller *FeatureFlagsPoller

	distinctIdsFeatureFlagsReported *lru.Cache[flagUser, struct{}]

	// Decider for feature flag methods
	decider decider
}

type flagUser struct {
	distinctID string
	flagKey    string
}

// Instantiate a new client that uses the write key passed as first argument to
// send messages to the backend.
// The client is created with the default configuration.
func New(apiKey string) Client {
	// Here we can ignore the error because the default config is always valid.
	c, _ := NewWithConfig(apiKey, Config{})
	return c
}

// NewWithConfig instantiate a new client that uses the write key and configuration passed
// as arguments to send messages to the backend.
// The function will return an error if the configuration contained impossible
// values (like a negative flush interval for example).
// When the function returns an error the returned client will always be nil.
func NewWithConfig(apiKey string, config Config) (cli Client, err error) {
	if err = config.Validate(); err != nil {
		return
	}

	config = makeConfig(config)
	reportedCache, err := lru.New[flagUser, struct{}](CACHE_DEFAULT_SIZE)
	if err != nil && config.Logger != nil {
		config.Logger.Errorf("Error creating cache for reported flags: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &client{
		Config:                          config,
		key:                             apiKey,
		msgs:                            make(chan preparedMessage, config.BatchSize*config.NumWorkers),
		batches:                         make(chan preparedBatch, config.NumWorkers),
		quit:                            make(chan struct{}),
		shutdown:                        make(chan struct{}),
		ctx:                             ctx,
		cancel:                          cancel,
		http:                            makeHttpClient(config.Transport, config.BatchUploadTimeout),
		distinctIdsFeatureFlagsReported: reportedCache,
	}

	c.decider, err = newFlagsClient(apiKey, config.Endpoint, c.http, config.FeatureFlagRequestTimeout, c.Logger)
	if err != nil {
		return nil, fmt.Errorf("error creating flags client: %v", err)
	}

	if len(c.PersonalApiKey) > 0 {
		c.featureFlagsPoller, err = newFeatureFlagsPoller(
			c.key,
			c.Config.PersonalApiKey,
			c.Logger,
			c.Endpoint,
			c.http,
			c.DefaultFeatureFlagsPollingInterval,
			c.NextFeatureFlagsPollingTick,
			c.FeatureFlagRequestTimeout,
			c.decider,
			c.Config.GetDisableGeoIP(),
		)
		if err != nil {
			return nil, err
		}
	}

	// Start fixed worker pool
	for i := 0; i < config.NumWorkers; i++ {
		go c.worker()
	}

	go c.loop()

	cli = c
	return
}

func makeHttpClient(transport http.RoundTripper, timeout time.Duration) http.Client {
	httpClient := http.Client{
		Transport: transport,
	}
	if supportsTimeout(transport) {
		httpClient.Timeout = timeout
	}
	return httpClient
}

func dereferenceMessage(msg Message) Message {
	switch m := msg.(type) {
	case *Alias:
		if m == nil {
			return nil
		}
		return *m
	case *Identify:
		if m == nil {
			return nil
		}
		return *m
	case *GroupIdentify:
		if m == nil {
			return nil
		}
		return *m
	case *Capture:
		if m == nil {
			return nil
		}
		return *m
	case *Exception:
		if m == nil {
			return nil
		}
		return *m
	}

	return msg
}

func (c *client) Enqueue(msg Message) (err error) {
	// Fast path: check if client is closed before doing any work
	if c.closed.Load() {
		return ErrClosed
	}

	msg = dereferenceMessage(msg)
	if err = msg.Validate(); err != nil {
		return
	}

	var ts = c.now()

	// Helper to send prepared message with panic recovery
	sendPrepared := func(prepared preparedMessage) {
		defer func() {
			// When the `msgs` channel is closed writing to it will trigger a panic.
			// To avoid letting the panic propagate to the caller we recover from it
			// and instead report that the client has been closed and shouldn't be
			// used anymore.
			if recover() != nil {
				err = ErrClosed
			}
		}()
		c.msgs <- prepared
	}

	switch m := msg.(type) {
	case Alias:
		m.Type = "alias"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		// Prepare message: serialize to JSON for efficient batch building
		data, apiMsg, serErr := prepareForSend(m)
		if serErr != nil {
			err = serErr
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg})
		return

	case Identify:
		m.Type = "identify"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		// Prepare message: serialize to JSON for efficient batch building
		data, apiMsg, serErr := prepareForSend(m)
		if serErr != nil {
			err = serErr
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg})
		return

	case GroupIdentify:
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		// Prepare message: serialize to JSON for efficient batch building
		data, apiMsg, serErr := prepareForSend(m)
		if serErr != nil {
			err = serErr
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg})
		return

	case Capture:
		m.Type = "capture"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		if m.shouldSendFeatureFlags() {
			// Add all feature variants to event
			personProperties := NewProperties()
			groupProperties := map[string]Properties{}
			opts := m.getFeatureFlagsOptions()

			// Use custom properties if provided via options
			if opts != nil {
				if opts.PersonProperties != nil {
					personProperties = opts.PersonProperties
				}
				if opts.GroupProperties != nil {
					groupProperties = opts.GroupProperties
				}
			}

			featureVariants, err := c.getFeatureVariantsWithOptions(m.DistinctId, m.Groups, personProperties, groupProperties, opts)
			if err != nil {
				c.Errorf("unable to get feature variants - %s", err)
			}

			if m.Properties == nil {
				m.Properties = NewProperties()
			}

			for feature, variant := range featureVariants {
				propKey := fmt.Sprintf("$feature/%s", feature)
				m.Properties[propKey] = variant
			}
			// Add all feature flag keys to $active_feature_flags key
			featureKeys := make([]string, len(featureVariants))
			i := 0
			for k := range featureVariants {
				featureKeys[i] = k
				i++
			}
			m.Properties["$active_feature_flags"] = featureKeys
		}
		if m.Properties == nil {
			m.Properties = NewProperties()
		}
		m.Properties.Merge(c.DefaultEventProperties)
		// Prepare message: serialize to JSON for efficient batch building
		data, apiMsg, serErr := prepareForSend(m)
		if serErr != nil {
			err = serErr
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg})
		return

	case Exception:
		m.Type = "exception"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		// Prepare message: serialize to JSON for efficient batch building
		data, apiMsg, serErr := prepareForSend(m)
		if serErr != nil {
			err = serErr
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg})
		return

	default:
		err = fmt.Errorf("messages with custom types cannot be enqueued: %T", msg)
		return
	}
}

func (c *client) IsFeatureEnabled(flagConfig FeatureFlagPayload) (interface{}, error) {
	if err := flagConfig.validate(); err != nil {
		return false, err
	}

	result, err := c.GetFeatureFlag(flagConfig)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (c *client) ReloadFeatureFlags() error {
	if c.featureFlagsPoller == nil {
		err := fmt.Errorf("cannot use feature flags: %w", ErrNoPersonalAPIKey)
		c.debugf(err.Error())
		return err
	}
	c.featureFlagsPoller.ForceReload()
	return nil
}

func (c *client) GetFeatureFlagPayload(flagConfig FeatureFlagPayload) (string, error) {
	if err := flagConfig.validate(); err != nil {
		return "", err
	}

	var payload string
	var err error

	if c.featureFlagsPoller != nil {
		// get feature flag from the poller, which uses the personal api key
		// this is only available when using a PersonalApiKey
		payload, err = c.featureFlagsPoller.GetFeatureFlagPayload(flagConfig)
	} else {
		// if there's no poller, get the feature flag from the flags endpoint
		c.debugf("getting feature flag from flags endpoint")
		payload, err = c.getFeatureFlagPayloadFromRemote(flagConfig.Key, flagConfig.DistinctId, flagConfig.Groups, flagConfig.PersonProperties, flagConfig.GroupProperties)
	}

	return payload, err
}

func (c *client) GetFeatureFlag(flagConfig FeatureFlagPayload) (interface{}, error) {
	return c.getFeatureFlagWithContext(context.Background(), flagConfig)
}

// getFeatureFlagWithContext returns variant value if multivariant flag or otherwise a boolean
// indicating if the given flag is on or off for the user.
// The context can be used to control timeouts and cancellation.
func (c *client) getFeatureFlagWithContext(ctx context.Context, flagConfig FeatureFlagPayload) (interface{}, error) {
	// Check context before starting
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if err := flagConfig.validate(); err != nil {
		return false, err
	}

	var flagValue interface{}
	var err error
	var flagResult *FeatureFlagResult

	if c.featureFlagsPoller != nil {
		// get feature flag from the poller, which uses the personal api key
		// this is only available when using a PersonalApiKey
		flagValue, err = c.featureFlagsPoller.GetFeatureFlag(flagConfig)
		flagResult = &FeatureFlagResult{
			Value: flagValue,
			Err:   err,
		}
	} else {
		// if there's no poller, get the feature flag from the flags endpoint
		c.debugf("getting feature flag from flags endpoint")
		flagResult = c.getFeatureFlagFromRemote(flagConfig.Key, flagConfig.DistinctId, flagConfig.Groups,
			flagConfig.PersonProperties, flagConfig.GroupProperties)
		flagValue = flagResult.Value
		err = flagResult.Err
		if f, ok := flagValue.(FlagDetail); ok {
			flagValue = f.GetValue()
			flagResult.Value = flagValue
		}
	}

	// Check context after flag evaluation
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	cacheKey := flagUser{flagConfig.DistinctId, flagConfig.Key}
	if *flagConfig.SendFeatureFlagEvents && !c.distinctIdsFeatureFlagsReported.Contains(cacheKey) {
		var properties = NewProperties().
			Set("$feature_flag", flagConfig.Key).
			Set("$feature_flag_response", flagValue).
			Set("$feature_flag_errored", err != nil)

		if flagResult.RequestID != nil {
			properties.Set("$feature_flag_request_id", *flagResult.RequestID)
		}

		if flagResult.EvaluatedAt != nil {
			properties.Set("$feature_flag_evaluated_at", *flagResult.EvaluatedAt)
		}

		if flagResult.FlagDetail != nil {
			properties.Set("$feature_flag_version", flagResult.FlagDetail.Metadata.Version)
			properties.Set("$feature_flag_id", flagResult.FlagDetail.Metadata.ID)
			if flagResult.FlagDetail.Reason != nil {
				properties.Set("$feature_flag_reason", flagResult.FlagDetail.Reason.Description)
			}
		}

		errorString := flagResult.GetErrorString()
		if errorString != "" {
			properties.Set("$feature_flag_error", errorString)
		}

		if c.Enqueue(Capture{
			DistinctId: flagConfig.DistinctId,
			Event:      "$feature_flag_called",
			Properties: properties,
			Groups:     flagConfig.Groups,
		}) == nil {
			c.distinctIdsFeatureFlagsReported.Add(cacheKey, struct{}{})
		}
	}

	return flagValue, err
}

func (c *client) GetRemoteConfigPayload(flagKey string) (string, error) {
	return c.makeRemoteConfigRequest(flagKey)
}

// ErrNoPersonalAPIKey is returned when oen tries to use feature flags without specifying a PersonalAPIKey
var ErrNoPersonalAPIKey = errors.New("no PersonalAPIKey provided")

// GetFeatureFlags returns all feature flag definitions used for local evaluation
// This is only available when using a PersonalApiKey. Not to be confused with
// GetAllFlags, which returns all flags and their values for a given user.
func (c *client) GetFeatureFlags() ([]FeatureFlag, error) {
	if c.featureFlagsPoller == nil {
		err := fmt.Errorf("cannot use feature flags: %w", ErrNoPersonalAPIKey)
		c.Logger.Debugf(err.Error())
		return nil, err
	}
	return c.featureFlagsPoller.GetFeatureFlags()
}

// GetAllFlags returns all flags and their values for a given user
// A flag value is either a boolean or a variant string (for multivariate flags)
// This first attempts local evaluation if a poller exists, otherwise it falls
// back to the flags endpoint
func (c *client) GetAllFlags(flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	return c.getAllFlagsWithContext(context.Background(), flagConfig)
}

// getAllFlagsWithContext returns all flags and their values for a given user.
// The context can be used to control timeouts and cancellation.
// A flag value is either a boolean or a variant string (for multivariate flags)
func (c *client) getAllFlagsWithContext(ctx context.Context, flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
	// Check context before starting
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := flagConfig.validate(); err != nil {
		return nil, err
	}

	var flagsValue map[string]interface{}
	var err error

	if c.featureFlagsPoller != nil {
		// get feature flags from the poller, which uses the personal api key
		// this is only available when using a PersonalApiKey
		flagsValue, err = c.featureFlagsPoller.GetAllFlags(flagConfig)
	} else {
		// if there's no poller, get the feature flags from the flags endpoint
		c.debugf("getting all feature flags from flags endpoint")
		flagsValue, err = c.getAllFeatureFlagsFromRemote(flagConfig.DistinctId, flagConfig.Groups,
			flagConfig.PersonProperties, flagConfig.GroupProperties)
	}

	// Check context after operation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return flagsValue, err
}

// Close gracefully shuts down the client, flushing any pending messages.
// It waits up to ShutdownTimeout for in-flight requests to complete.
// Close is safe to call multiple times; subsequent calls return ErrClosed.
func (c *client) Close() error {
	if c.ShutdownTimeout < 0 {
		// Negative timeout means wait indefinitely
		return c.CloseWithContext(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	return c.CloseWithContext(ctx)
}

// CloseWithContext gracefully shuts down the client with the provided context.
// The context can be used to control the shutdown deadline. If the context
// is cancelled before shutdown completes, in-flight requests may be aborted.
// CloseWithContext is safe to call multiple times; subsequent calls return ErrClosed.
func (c *client) CloseWithContext(ctx context.Context) error {
	var err error
	alreadyClosed := true

	c.closeOnce.Do(func() {
		alreadyClosed = false
		// Mark as closed to fast-fail new Enqueue calls
		c.closed.Store(true)

		// Signal the batch loop to stop and drain
		close(c.quit)

		// Wait for shutdown with timeout from provided context
		select {
		case <-c.shutdown:
			// Clean shutdown completed
			c.debugf("shutdown completed successfully")
		case <-ctx.Done():
			// Timeout exceeded - cancel client context to abort in-flight requests
			c.cancel()
			err = fmt.Errorf("shutdown timeout: %w", ctx.Err())
			c.Warnf("shutdown timeout exceeded, some messages may be lost")
			// Wait for shutdown to acknowledge cancellation
			<-c.shutdown
		}
	})

	if alreadyClosed {
		return ErrClosed
	}
	return err
}

// worker consumes batches from channel until closed.
func (c *client) worker() {
	for batch := range c.batches {
		c.processBatch(batch)
	}
}

// processBatch handles a single batch with guaranteed counter decrement.
func (c *client) processBatch(batch preparedBatch) {
	// CRITICAL: defer ensures counter decrements on ANY exit path
	// (success, JSON error, HTTP error, panic, context cancellation)
	defer c.inFlight.Add(-1)

	// Recover from panics to prevent worker death
	defer func() {
		if err := recover(); err != nil {
			c.Errorf("panic in worker: %v", err)
			c.notifyFailure(batch.msgs, fmt.Errorf("panic: %v", err))
		}
	}()

	c.send(batch)
}

// sendBatch attempts to send a batch to the worker pool.
// Returns true if successful, false if the worker pool is at capacity.
func (c *client) sendBatch(batch preparedBatch) bool {
	c.inFlight.Add(1)
	select {
	case c.batches <- batch:
		return true
	default:
		// Channel full - reject batch
		c.inFlight.Add(-1)
		return false
	}
}

// awaitDrain polls until all in-flight batches complete or context times out.
func (c *client) awaitDrain(ctx context.Context) error {
	for c.inFlight.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// continue polling
		}
	}
	c.debugf("all in-flight batches completed")
	return nil
}

// send encodes batch wrapper and performs HTTP POST with retries.
// Messages are already pre-serialized, so only the batch wrapper is serialized here.
func (c *client) send(pb preparedBatch) {
	// Build batch with pre-serialized messages - only the wrapper is serialized here
	b, err := json.Marshal(batch{
		ApiKey:              c.key,
		HistoricalMigration: c.HistoricalMigration,
		Messages:            pb.data,
	})

	if err != nil {
		c.Errorf("marshalling batch wrapper - %s", err)
		c.notifyFailure(pb.msgs, err)
		return
	}

	for i := 0; i < c.maxAttempts; i++ {
		if err = c.upload(c.ctx, b); err == nil {
			c.notifySuccess(pb.msgs)
			return
		}

		// Check if context is cancelled (shutdown timeout exceeded)
		if c.ctx.Err() != nil {
			c.Errorf("%d messages dropped: shutdown timeout", len(pb.msgs))
			c.notifyFailure(pb.msgs, err)
			return
		}

		// Wait for retry or shutdown
		select {
		case <-time.After(c.RetryAfter(i)):
			// continue to next attempt
		case <-c.ctx.Done():
			c.notifyFailure(pb.msgs, err)
			return
		}
	}

	c.Errorf("%d messages dropped after %d attempts", len(pb.msgs), c.maxAttempts)
	c.notifyFailure(pb.msgs, err)
}

// Upload serialized batch message.
func (c *client) upload(ctx context.Context, b []byte) error {
	url := c.Endpoint + "/batch/"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		c.Errorf("creating request - %s", err)
		return err
	}

	version := getVersion()

	req.Header.Add("User-Agent", SDKName+"/"+version)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", fmt.Sprintf("%d", len(b)))

	res, err := c.http.Do(req)
	if err != nil {
		c.Warnf("sending request - %s", err)
		return err
	}

	defer res.Body.Close()
	return c.report(res)
}

// Report on response body.
func (c *client) report(res *http.Response) (err error) {
	var body []byte

	if res.StatusCode < 300 {
		c.debugf("response %s", res.Status)
		return
	}

	if body, err = io.ReadAll(res.Body); err != nil {
		c.Errorf("response %d %s - %s", res.StatusCode, res.Status, err)
		return
	}

	c.Logger.Logf("response %d %s – %s", res.StatusCode, res.Status, string(body))
	return fmt.Errorf("%d %s", res.StatusCode, res.Status)
}

// loop processes messages from the msgs channel, batches them by size,
// and sends batches to the worker pool.
func (c *client) loop() {
	defer close(c.batches) // signal workers to exit
	defer close(c.shutdown)
	if c.featureFlagsPoller != nil {
		defer c.featureFlagsPoller.shutdownPoller()
	}

	var batchData []json.RawMessage
	var batchMsgs []APIMessage
	var batchSize int

	tick := time.NewTicker(c.Interval)
	defer tick.Stop()

	// Helper to create and send a batch
	flushBatch := func() bool {
		if len(batchData) == 0 {
			return true
		}
		batch := preparedBatch{data: batchData, msgs: batchMsgs}
		if !c.sendBatch(batch) {
			c.Errorf("sending batch failed - %s", ErrTooManyRequests)
			c.notifyFailure(batchMsgs, ErrTooManyRequests)
			return false
		}
		return true
	}

	// Helper to reset batch state
	resetBatch := func() {
		batchData, batchMsgs, batchSize = nil, nil, 0
	}

	for {
		select {
		case prepared := <-c.msgs:
			msgSize := len(prepared.data)

			// Early reject oversized messages
			if msgSize > maxMessageBytes {
				c.Errorf("message exceeds maximum size (%d > %d)", msgSize, maxMessageBytes)
				c.notifyFailure([]APIMessage{prepared.msg}, ErrMessageTooBig)
				continue
			}

			// Flush current batch if adding this message would exceed byte limit
			if batchSize+msgSize > maxBatchBytes && len(batchData) > 0 {
				c.debugf("batch size limit reached (%d bytes) – flushing %d messages", batchSize, len(batchData))
				flushBatch()
				resetBatch()
			}

			// Add pre-serialized data and original message to batch
			batchData = append(batchData, prepared.data)
			batchMsgs = append(batchMsgs, prepared.msg)
			batchSize += msgSize
			c.debugf("buffer (%d/%d, %d bytes) %v", len(batchData), c.BatchSize, batchSize, prepared.msg)

			// Flush if batch count limit reached
			if len(batchData) >= c.BatchSize {
				c.debugf("batch count limit reached – flushing %d messages", len(batchData))
				flushBatch()
				resetBatch()
			}

		case <-tick.C:
			if len(batchData) > 0 {
				c.debugf("interval flush – sending %d messages", len(batchData))
				flushBatch()
				resetBatch()
			}

		case <-c.quit:
			c.debugf("shutdown requested – draining messages")

			// Close msgs channel to stop accepting new messages
			close(c.msgs)

			// Drain remaining messages using same logic as normal processing
			for prepared := range c.msgs {
				msgSize := len(prepared.data)

				if msgSize > maxMessageBytes {
					c.Errorf("message exceeds maximum size (%d > %d)", msgSize, maxMessageBytes)
					c.notifyFailure([]APIMessage{prepared.msg}, ErrMessageTooBig)
					continue
				}

				if batchSize+msgSize > maxBatchBytes && len(batchData) > 0 {
					flushBatch()
					resetBatch()
				}

				batchData = append(batchData, prepared.data)
				batchMsgs = append(batchMsgs, prepared.msg)
				batchSize += msgSize

				if len(batchData) >= c.BatchSize {
					flushBatch()
					resetBatch()
				}
			}

			// Flush any remaining messages
			if len(batchData) > 0 {
				c.debugf("flushing final batch of %d messages", len(batchData))
				flushBatch()
			}

			// Wait for workers with timeout
			if err := c.awaitDrain(c.ctx); err != nil {
				c.Warnf("shutdown timeout: %d batches still in flight", c.inFlight.Load())
			}

			c.debugf("shutdown complete")
			return
		}
	}
}

func (c *client) debugf(format string, args ...interface{}) {
	c.Logger.Debugf(format, args...)
}

func (c *client) Errorf(format string, args ...interface{}) {
	c.Logger.Errorf(format, args...)
}

func (c *client) Warnf(format string, args ...interface{}) {
	c.Logger.Warnf(format, args...)
}

func (c *client) notifySuccess(msgs []APIMessage) {
	if c.Callback != nil {
		for _, m := range msgs {
			c.Callback.Success(m)
		}
	}
}

func (c *client) notifyFailure(msgs []APIMessage, err error) {
	if c.Callback != nil {
		for _, m := range msgs {
			c.Callback.Failure(m, err)
		}
	}
}

func (c *client) getFeatureVariants(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (map[string]interface{}, error) {
	return c.getFeatureVariantsWithOptions(distinctId, groups, personProperties, groupProperties, nil)
}

func (c *client) getFeatureVariantsWithOptions(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties, options *SendFeatureFlagsOptions) (map[string]interface{}, error) {
	if c.featureFlagsPoller == nil {
		errorMessage := "specifying a PersonalApiKey is required for using feature flags"
		c.Errorf(errorMessage)
		return nil, errors.New(errorMessage)
	}

	// If OnlyEvaluateLocally is set, only use local evaluation
	if options != nil && options.OnlyEvaluateLocally {
		return c.featureFlagsPoller.getFeatureFlagVariantsLocalOnly(distinctId, groups, personProperties, groupProperties)
	}

	featureVariants, err := c.featureFlagsPoller.getFeatureFlagVariants(distinctId, groups, personProperties, groupProperties)
	if err != nil {
		return nil, err
	}
	return featureVariants.FeatureFlags, nil
}

func (c *client) makeRemoteConfigRequest(flagKey string) (string, error) {
	baseURL, err := url.JoinPath(c.Endpoint, "api/projects/@current/feature_flags", flagKey, "remote_config")
	if err != nil {
		return "", fmt.Errorf("building URL: %v", err)
	}

	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %v", err)
	}

	q := parsedURL.Query()
	q.Set("token", c.key)
	parsedURL.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", parsedURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.PersonalApiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "posthog-go/"+Version)

	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code from %s: %d", parsedURL.String(), res.StatusCode)
	}

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response from /remote_config/: %v", err)
	}

	var responseData string
	if err := json.Unmarshal(resBody, &responseData); err != nil {
		return "", fmt.Errorf("error parsing JSON response from /remote_config/: %v", err)
	}
	return responseData, nil
}

// isFeatureFlagsQuotaLimited checks if feature flags are quota limited in the flags response
func (c *client) isFeatureFlagsQuotaLimited(flagsResponse *FlagsResponse) bool {
	if flagsResponse.QuotaLimited == nil {
		return false
	}
	for _, limitedFeature := range flagsResponse.QuotaLimited {
		if limitedFeature == "feature_flags" {
			c.Logger.Warnf("[FEATURE FLAGS] PostHog feature flags quota limited. Learn more about billing limits at https://posthog.com/docs/billing/limits-alerts")
			return true
		}
	}
	return false
}

func (c *client) getFeatureFlagFromRemote(key string, distinctId string, groups Groups, personProperties Properties,
	groupProperties map[string]Properties) *FeatureFlagResult {

	result := &FeatureFlagResult{
		Value: false,
	}

	flagsResponse, err := c.decider.makeFlagsRequest(distinctId, groups, personProperties, groupProperties, c.GetDisableGeoIP())

	if err != nil {
		result.Err = err
		return result
	}

	if flagsResponse == nil {
		return result
	}

	result.RequestID = &flagsResponse.RequestId
	result.EvaluatedAt = flagsResponse.EvaluatedAt
	result.ErrorsWhileComputingFlags = flagsResponse.ErrorsWhileComputingFlags
	result.QuotaLimited = c.isFeatureFlagsQuotaLimited(flagsResponse)

	if result.QuotaLimited {
		return result
	}

	if flagDetail, ok := flagsResponse.Flags[key]; ok {
		result.Value = flagDetail
		result.FlagDetail = &flagDetail
		result.FlagMissing = false
	} else {
		result.Value = false
		result.FlagMissing = true
	}

	return result
}

func (c *client) getFeatureFlagPayloadFromRemote(key string, distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (string, error) {
	flagsResponse, err := c.decider.makeFlagsRequest(distinctId, groups, personProperties, groupProperties, c.GetDisableGeoIP())
	if err != nil {
		return "", err
	}

	if c.isFeatureFlagsQuotaLimited(flagsResponse) {
		return "", nil
	}

	if value, ok := flagsResponse.FeatureFlagPayloads[key]; ok {
		return value, nil
	}

	return "", nil
}

func (c *client) getAllFeatureFlagsFromRemote(distinctId string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (map[string]interface{}, error) {
	flagsResponse, err := c.decider.makeFlagsRequest(distinctId, groups, personProperties, groupProperties, c.GetDisableGeoIP())
	if err != nil {
		return nil, err
	}

	if c.isFeatureFlagsQuotaLimited(flagsResponse) {
		return map[string]interface{}{}, nil
	}

	return flagsResponse.FeatureFlags, nil
}
