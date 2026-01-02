package posthog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
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

	// GetFeatureFlagWithContext returns variant value if multivariant flag or otherwise a boolean
	// indicating if the given flag is on or off for the user.
	// The context can be used to control the request timeout.
	GetFeatureFlagWithContext(context.Context, FeatureFlagPayload) (interface{}, error)

	// GetAllFlagsWithContext returns all flags for a user.
	// The context can be used to control the request timeout.
	GetAllFlagsWithContext(context.Context, FeatureFlagPayloadNoKey) (map[string]interface{}, error)
}

type client struct {
	Config
	key string

	// This channel is where the `Enqueue` method writes messages so they can be
	// picked up and pushed by the backend goroutine taking care of applying the
	// batching rules.
	msgs chan APIMessage

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
		msgs:                            make(chan APIMessage, 100),
		quit:                            make(chan struct{}),
		shutdown:                        make(chan struct{}),
		ctx:                             ctx,
		cancel:                          cancel,
		http:                            makeHttpClient(config.Transport),
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

	go c.loop()

	cli = c
	return
}

func makeHttpClient(transport http.RoundTripper) http.Client {
	httpClient := http.Client{
		Transport: transport,
	}
	if supportsTimeout(transport) {
		httpClient.Timeout = 10 * time.Second
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

	switch m := msg.(type) {
	case Alias:
		m.Type = "alias"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		msg = m

	case Identify:
		m.Type = "identify"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		msg = m

	case GroupIdentify:
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		msg = m

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
		msg = m

	case Exception:
		m.Type = "exception"
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		msg = m

	default:
		err = fmt.Errorf("messages with custom types cannot be enqueued: %T", msg)
		return
	}

	defer func() {
		// When the `msgs` channel is closed writing to it will trigger a panic.
		// To avoid letting the panic propagate to the caller we recover from it
		// and instead report that the client has been closed and shouldn't be
		// used anymore.
		if recover() != nil {
			err = ErrClosed
		}
	}()

	c.msgs <- msg.APIfy()

	return
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
	return c.GetFeatureFlagWithContext(context.Background(), flagConfig)
}

// GetFeatureFlagWithContext returns variant value if multivariant flag or otherwise a boolean
// indicating if the given flag is on or off for the user.
// The context can be used to control timeouts and cancellation.
func (c *client) GetFeatureFlagWithContext(ctx context.Context, flagConfig FeatureFlagPayload) (interface{}, error) {
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
	return c.GetAllFlagsWithContext(context.Background(), flagConfig)
}

// GetAllFlagsWithContext returns all flags and their values for a given user.
// The context can be used to control timeouts and cancellation.
// A flag value is either a boolean or a variant string (for multivariate flags)
func (c *client) GetAllFlagsWithContext(ctx context.Context, flagConfig FeatureFlagPayloadNoKey) (map[string]interface{}, error) {
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

// Asynchronously send a batched requests.
func (c *client) sendAsync(msgs []message, wg *sync.WaitGroup, ex *executor) {
	wg.Add(1)

	if !ex.do(func() {
		defer wg.Done()
		defer func() {
			// In case a bug is introduced in the send function that triggers
			// a panic, we don't want this to ever crash the application so we
			// catch it here and log it instead.
			if err := recover(); err != nil {
				c.Errorf("panic - %s", err)
			}
		}()
		c.send(msgs)
	}) {
		wg.Done()
		c.Errorf("sending messages failed - %s", ErrTooManyRequests)
		c.notifyFailure(msgs, ErrTooManyRequests)
	}
}

// Send batch request.
func (c *client) send(msgs []message) {
	b, err := json.Marshal(batch{
		ApiKey:              c.key,
		HistoricalMigration: c.HistoricalMigration,
		Messages:            msgs,
	})

	if err != nil {
		c.Errorf("marshalling messages - %s", err)
		c.notifyFailure(msgs, err)
		return
	}

	for i := 0; i != c.maxAttempts; i++ {
		if err = c.upload(c.ctx, b); err == nil {
			c.notifySuccess(msgs)
			return
		}

		// Check if context is cancelled (hard shutdown timeout exceeded)
		if c.ctx.Err() != nil {
			c.Errorf("%d messages dropped because shutdown timeout exceeded", len(msgs))
			c.notifyFailure(msgs, err)
			return
		}

		// Wait for either a retry timeout or the client to be closed
		select {
		case <-time.After(c.RetryAfter(i)):
			// Continue to next retry attempt
		case <-c.quit:
			c.Errorf("%d messages dropped because they failed to be sent and the client was closed", len(msgs))
			c.notifyFailure(msgs, err)
			return
		}
	}

	c.Errorf("%d messages dropped because they failed to be sent after %d attempts", len(msgs), c.maxAttempts)
	c.notifyFailure(msgs, err)
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

// Batch loop.
func (c *client) loop() {
	defer close(c.shutdown)
	if c.featureFlagsPoller != nil {
		defer c.featureFlagsPoller.shutdownPoller()
	}

	wg := &sync.WaitGroup{}

	tick := time.NewTicker(c.Interval)
	defer tick.Stop()

	ex := newExecutor(c.maxConcurrentRequests)
	defer ex.close()

	mq := messageQueue{
		maxBatchSize:  c.BatchSize,
		maxBatchBytes: c.maxBatchBytes(),
	}

	for {
		select {
		case msg := <-c.msgs:
			c.push(&mq, msg, wg, ex)

		case <-tick.C:
			c.flush(&mq, wg, ex)

		case <-c.quit:
			c.debugf("shutdown requested – draining messages")

			// Drain the msg channel, we have to close it first so no more
			// messages can be pushed and otherwise the loop would never end.
			close(c.msgs)
			for msg := range c.msgs {
				c.push(&mq, msg, wg, ex)
			}

			// Flush any remaining messages in the queue
			c.flush(&mq, wg, ex)

			// Wait for in-flight requests to complete, respecting shutdown context
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				c.debugf("all in-flight requests completed")
			case <-c.ctx.Done():
				c.debugf("shutdown timeout, some requests may still be in flight")
			}

			c.debugf("shutdown complete")
			return
		}
	}
}

func (c *client) push(q *messageQueue, m APIMessage, wg *sync.WaitGroup, ex *executor) {
	var msg message
	var err error

	if msg, err = makeMessage(m, maxMessageBytes); err != nil {
		c.Errorf("%s - %v", err, m)
		c.notifyFailure([]message{{m, nil}}, err)
		return
	}

	c.debugf("buffer (%d/%d) %v", len(q.pending), c.BatchSize, m)

	if msgs := q.push(msg); msgs != nil {
		c.debugf("exceeded messages batch limit with batch of %d messages – flushing", len(msgs))
		c.sendAsync(msgs, wg, ex)
	}
}

func (c *client) flush(q *messageQueue, wg *sync.WaitGroup, ex *executor) {
	if msgs := q.flush(); msgs != nil {
		c.debugf("flushing %d messages", len(msgs))
		c.sendAsync(msgs, wg, ex)
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

func (c *client) maxBatchBytes() int {
	b, _ := json.Marshal(batch{
		Messages: []message{},
	})
	return maxBatchBytes - len(b)
}

func (c *client) notifySuccess(msgs []message) {
	if c.Callback != nil {
		for _, m := range msgs {
			c.Callback.Success(m.msg)
		}
	}
}

func (c *client) notifyFailure(msgs []message, err error) {
	if c.Callback != nil {
		for _, m := range msgs {
			c.Callback.Failure(m.msg, err)
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
