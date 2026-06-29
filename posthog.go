package posthog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	json "github.com/goccy/go-json"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	unimplementedError = "not implemented"
	// CACHE_DEFAULT_SIZE is the number of feature flag calls tracked for local deduplication.
	CACHE_DEFAULT_SIZE = 300_000

	propertyGeoipDisable = "$geoip_disable"
	propertyIsServer     = "$is_server"

	// DefaultIdleConns is the default max idle connections for the HTTP client.
	DefaultIdleConns = 100
	// DefaultIdleConnsPerHost is the default max idle connections per host.
	// This is higher than http.DefaultTransport's value of 2 to reduce
	// connection churn when sending batches.
	DefaultIdleConnsPerHost = 10
)

// EnqueueClient is the minimal interface implemented by clients that can queue PostHog messages.
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
	// Enqueue returns an error if the message could not be queued, which happens
	// when the client is closed or the message is invalid.
	Enqueue(Message) error
}

// Client interface is the main API exposed by the posthog package.
// Values that satisfy this interface are returned by the client constructors
// provided by the package and provide a way to send messages via the HTTP API.
type Client interface {
	EnqueueClient

	// Close gracefully shuts down the client and flushes pending messages.
	// If Config.ShutdownTimeout is positive, Close uses it as the shutdown deadline;
	// otherwise it waits indefinitely. Repeated calls return ErrClosed.
	Close() error

	// IsFeatureEnabled evaluates one feature flag for a user and returns the same
	// value as GetFeatureFlag: a variant string for multivariate flags or a bool
	// for boolean flags. The FeatureFlagPayload parameter supplies the flag key,
	// distinct ID, optional groups/properties, and evaluation options.
	// Deprecated: Prefer EvaluateFlags for new code.
	IsFeatureEnabled(FeatureFlagPayload) (interface{}, error)

	// GetFeatureFlag evaluates one feature flag for a user. It returns a variant
	// string for multivariate flags, true or false for boolean flags, false with
	// nil error when the flag is missing/disabled, and an error for evaluation failures.
	// Deprecated: Prefer EvaluateFlags for new code.
	GetFeatureFlag(FeatureFlagPayload) (interface{}, error)

	// GetFeatureFlagResult evaluates one feature flag and returns its value and payload together.
	// Use this instead of calling GetFeatureFlag and GetFeatureFlagPayload separately.
	// It returns ErrFlagNotFound when the flag cannot be found or evaluated and
	// ErrNoPersonalAPIKey when OnlyEvaluateLocally is true without PersonalApiKey.
	GetFeatureFlagResult(FeatureFlagPayload) (*FeatureFlagResult, error)

	// GetFeatureFlagPayload returns the payload for the matching flag value, or an
	// empty string when no payload is configured or the flag is missing/disabled.
	// Deprecated: Use GetFeatureFlagResult instead, which returns both the flag value
	// and payload while properly tracking feature flag usage.
	GetFeatureFlagPayload(FeatureFlagPayload) (string, error)

	// GetRemoteConfigPayload returns the decrypted payload for a remote config flag key.
	// It requires Config.PersonalApiKey and returns ErrNoPersonalAPIKey when missing.
	GetRemoteConfigPayload(string) (string, error)

	// GetAllFlags evaluates all flags for a user. Returned values are booleans for
	// boolean flags and strings for multivariate variants. It returns ErrNoPersonalAPIKey
	// when OnlyEvaluateLocally is true without PersonalApiKey.
	GetAllFlags(FeatureFlagPayloadNoKey) (map[string]interface{}, error)

	// EvaluateFlags returns a snapshot of feature-flag evaluations for the
	// given distinct_id using at most one /flags request. Returns ErrNoDistinctID
	// if DistinctId is empty. Pass the returned snapshot to a Capture event via
	// Capture.Flags to attach $feature/<key> properties without another network call.
	// Calls to IsEnabled and GetFlag
	// on the snapshot fire deduped $feature_flag_called events; GetFlagPayload
	// does not.
	//
	// If the remote /flags request fails after some flags were resolved
	// locally, EvaluateFlags returns a non-nil snapshot containing the
	// locally-evaluated flags alongside the error so the caller can still
	// branch on what was resolved.
	// If OnlyEvaluateLocally is true and no PersonalApiKey is configured, returns ErrNoPersonalAPIKey.
	EvaluateFlags(EvaluateFlagsPayload) (*FeatureFlagEvaluations, error)

	// ReloadFeatureFlags forces a reload of feature flags.
	// If no PersonalApiKey is configured, returns ErrNoPersonalAPIKey.
	ReloadFeatureFlags() error

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
	// uuid is the per-event UUID, used by the capture-v1 path to correlate
	// per-event results. Empty on the legacy path.
	uuid string
}

// preparedBatch holds both raw data for efficient serialization and
// original API messages for callbacks.
type preparedBatch struct {
	data []json.RawMessage // pre-serialized messages for batch submission
	msgs []APIMessage      // original messages for callbacks
	// uuids holds the per-event UUID aligned with data/msgs, used by the
	// capture-v1 send path to correlate per-event results. It is unused (nil)
	// on the legacy path.
	uuids []string
}

type client struct {
	Config
	key string

	// This channel is where the `Enqueue` method writes messages so they can be
	// picked up and pushed by the backend goroutine taking care of applying the
	// batching rules. Messages are pre-converted to APIMessage format with
	// pre-computed size to avoid race conditions.
	msgs chan preparedMessage

	// Channel for sending batches to workers. Acts as both a queue and
	// concurrency limiter - when full, new batches are shed via failure callback.
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

	// capture is the wire-protocol strategy (legacy /batch/ or analytics-v1),
	// chosen once in NewWithConfig from Config.CaptureMode.
	capture capturer
}

type flagUser struct {
	distinctID string
	flagKey    string
	flagValue  string
	deviceID   string
	// canonical JSON of the groups map (keys sorted) — empty when no groups
	// were passed. Lets the same `(user, flag, value)` fire a separate
	// `$feature_flag_called` event for each distinct group context, so
	// group-scoped flags don't undercount exposures when a user is evaluated
	// under multiple groups in the same process.
	groupsRepr string
}

// New creates a Client with the default Config and the provided PostHog project API key.
//
// The apiKey parameter is trimmed before use. If it is empty, New returns a
// no-op client whose methods return default values and ErrSDKDisabled where applicable.
func New(apiKey string) Client {
	// Here we can ignore the error because the default config is always valid.
	c, _ := NewWithConfig(apiKey, Config{})
	return c
}

// NewWithConfig creates a Client with the provided PostHog project API key and Config.
//
// The apiKey, Config.Endpoint, and Config.PersonalApiKey values are trimmed before use.
// It returns a ConfigError when config contains invalid values such as negative
// intervals or out-of-range retry settings; in that case the returned Client is nil.
// If apiKey is empty after trimming, NewWithConfig returns a no-op client and nil error.
func NewWithConfig(apiKey string, config Config) (cli Client, err error) {
	if err = config.Validate(); err != nil {
		return
	}

	config = makeConfig(config)
	apiKey = strings.TrimSpace(apiKey)
	if len(apiKey) == 0 {
		config.Logger.Errorf("posthog apiKey is empty after trimming whitespace; %s", ErrSDKDisabled)
		return newNoopClient(config), nil
	}
	reportedCache, err := lru.New[flagUser, struct{}](CACHE_DEFAULT_SIZE)
	if err != nil && config.Logger != nil {
		config.Logger.Errorf("Error creating cache for reported flags: %v", err)
	}

	// Channel sizing:
	// - batches queue sized by MaxEnqueuedRequests (default 1000)
	// - msgs queue sized to hold multiple batches worth of messages
	batchesQueueSize := config.MaxEnqueuedRequests
	msgQueueSize := max(100, config.BatchSize*10)

	ctx, cancel := context.WithCancel(context.Background())
	c := &client{
		Config:                          config,
		key:                             apiKey,
		msgs:                            make(chan preparedMessage, msgQueueSize),
		batches:                         make(chan preparedBatch, batchesQueueSize),
		quit:                            make(chan struct{}),
		shutdown:                        make(chan struct{}),
		ctx:                             ctx,
		cancel:                          cancel,
		http:                            makeHttpClient(config.Transport, config.BatchUploadTimeout),
		distinctIdsFeatureFlagsReported: reportedCache,
	}

	if config.CaptureMode == CaptureModeAnalyticsV1 {
		c.capture = analyticsV1Capturer{c}
	} else {
		c.capture = legacyCapturer{c}
	}

	c.decider, err = newFlagsClient(apiKey, config.Endpoint, c.http, config.FeatureFlagRequestTimeout, c.Logger, config.FeatureFlagRequestMaxRetries)
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

func makeHttpClient(transport http.RoundTripper, timeout time.Duration) http.Client {
	// If no custom transport provided, clone DefaultTransport with tuned connection pool
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConns = DefaultIdleConns
		t.MaxIdleConnsPerHost = DefaultIdleConnsPerHost
		transport = t
	}

	httpClient := http.Client{
		Transport: transport,
	}
	if supportsTimeout(transport) {
		httpClient.Timeout = timeout
	}
	return httpClient
}

func cloneMessageProperties(properties Properties) Properties {
	if properties == nil {
		return nil
	}
	clone := make(Properties, len(properties))
	for key, value := range properties {
		clone[key] = cloneMessagePropertyValue(value)
	}
	return clone
}

func cloneMessageGroups(groups Groups) Groups {
	if groups == nil {
		return nil
	}
	clone := make(Groups, len(groups))
	for key, value := range groups {
		clone[key] = cloneMessagePropertyValue(value)
	}
	return clone
}

func cloneMessagePropertyValue(value interface{}) interface{} {
	switch v := value.(type) {
	case Properties:
		return cloneMessageProperties(v)
	case Groups:
		return cloneMessageGroups(v)
	case map[string]interface{}:
		clone := make(map[string]interface{}, len(v))
		for key, nested := range v {
			clone[key] = cloneMessagePropertyValue(nested)
		}
		return clone
	case map[string]string:
		clone := make(map[string]string, len(v))
		for key, nested := range v {
			clone[key] = nested
		}
		return clone
	case map[string]bool:
		clone := make(map[string]bool, len(v))
		for key, nested := range v {
			clone[key] = nested
		}
		return clone
	case map[string]int:
		clone := make(map[string]int, len(v))
		for key, nested := range v {
			clone[key] = nested
		}
		return clone
	case map[string]int64:
		clone := make(map[string]int64, len(v))
		for key, nested := range v {
			clone[key] = nested
		}
		return clone
	case map[string]float64:
		clone := make(map[string]float64, len(v))
		for key, nested := range v {
			clone[key] = nested
		}
		return clone
	case []interface{}:
		clone := make([]interface{}, len(v))
		for i, nested := range v {
			clone[i] = cloneMessagePropertyValue(nested)
		}
		return clone
	case []string:
		return append([]string(nil), v...)
	case []bool:
		return append([]bool(nil), v...)
	case []int:
		return append([]int(nil), v...)
	case []int64:
		return append([]int64(nil), v...)
	case []float64:
		return append([]float64(nil), v...)
	}
	return value
}

func cloneExceptionFingerprint(fingerprint *string) *string {
	if fingerprint == nil {
		return nil
	}
	clone := *fingerprint
	return &clone
}

func cloneExceptionList(items []ExceptionItem) []ExceptionItem {
	if items == nil {
		return nil
	}
	clone := make([]ExceptionItem, len(items))
	for i, item := range items {
		if item.Mechanism != nil {
			mechanism := *item.Mechanism
			if item.Mechanism.Handled != nil {
				handled := *item.Mechanism.Handled
				mechanism.Handled = &handled
			}
			if item.Mechanism.Synthetic != nil {
				synthetic := *item.Mechanism.Synthetic
				mechanism.Synthetic = &synthetic
			}
			item.Mechanism = &mechanism
		}
		if item.Stacktrace != nil {
			stacktrace := *item.Stacktrace
			if item.Stacktrace.Frames != nil {
				stacktrace.Frames = append([]StackFrame(nil), item.Stacktrace.Frames...)
			}
			item.Stacktrace = &stacktrace
		}
		clone[i] = item
	}
	return clone
}

func isolateBeforeSendMessage(msg Message) Message {
	switch m := msg.(type) {
	case Identify:
		m.Properties = cloneMessageProperties(m.Properties)
		return m
	case GroupIdentify:
		m.Properties = cloneMessageProperties(m.Properties)
		return m
	case Capture:
		m.Properties = cloneMessageProperties(m.Properties)
		m.Groups = cloneMessageGroups(m.Groups)
		m.SendFeatureFlags = nil
		m.Flags = nil
		return m
	case Exception:
		m.Properties = cloneMessageProperties(m.Properties)
		m.ExceptionList = cloneExceptionList(m.ExceptionList)
		m.ExceptionFingerprint = cloneExceptionFingerprint(m.ExceptionFingerprint)
		return m
	}
	return msg
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

func (c *client) processBeforeSend(msg Message) (Message, bool) {
	if c.BeforeSend == nil {
		return msg, true
	}

	messageType := fmt.Sprintf("%T", msg)
	next, ok := c.runBeforeSendHook(c.BeforeSend, isolateBeforeSendMessage(msg), messageType)
	if !ok {
		return nil, false
	}
	if next == nil {
		c.Warnf("BeforeSend returned nil for %s; dropping message", messageType)
		return nil, false
	}

	next = dereferenceMessage(next)
	if next == nil {
		c.Warnf("BeforeSend returned nil for %s; dropping message", messageType)
		return nil, false
	}
	if nextType := fmt.Sprintf("%T", next); nextType != messageType {
		c.Errorf("BeforeSend returned %s instead of %s; dropping message", nextType, messageType)
		return nil, false
	}
	if err := next.Validate(); err != nil {
		c.Errorf("BeforeSend returned invalid %s: %v; dropping message", messageType, err)
		return nil, false
	}
	return next, true
}

func (c *client) runBeforeSendHook(hook BeforeSendFunc, msg Message, messageType string) (next Message, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			c.Errorf("panic in BeforeSend hook for %s: %v; dropping message", messageType, r)
			next = nil
			ok = false
		}
	}()
	return hook(msg), true
}

func (c *client) Enqueue(msg Message) error {
	return c.EnqueueWithContext(context.Background(), msg)
}

func (c *client) EnqueueWithContext(ctx context.Context, msg Message) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Fast path: check if client is closed before doing any work
	if c.closed.Load() {
		return ErrClosed
	}

	msg = dereferenceMessage(msg)

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
		if err = m.Validate(); err != nil {
			return
		}
		m.Type = "alias"
		m.Uuid = makeUUID(m.Uuid)
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		m.IsServer = c.GetIsServer()
		processed, shouldSend := c.processBeforeSend(m)
		if !shouldSend {
			return nil
		}
		m = processed.(Alias)
		data, apiMsg, eventUuid, serErr := c.capture.prepare(m)
		if serErr != nil {
			c.notifyFailure([]APIMessage{apiMsg}, serErr)
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg, uuid: eventUuid})
		return

	case Identify:
		if err = m.Validate(); err != nil {
			return
		}
		m.Type = "identify"
		m.Uuid = makeUUID(m.Uuid)
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		m.IsServer = c.GetIsServer()
		processed, shouldSend := c.processBeforeSend(m)
		if !shouldSend {
			return nil
		}
		m = processed.(Identify)
		data, apiMsg, eventUuid, serErr := c.capture.prepare(m)
		if serErr != nil {
			c.notifyFailure([]APIMessage{apiMsg}, serErr)
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg, uuid: eventUuid})
		return

	case GroupIdentify:
		if err = m.Validate(); err != nil {
			return
		}
		m.Uuid = makeUUID(m.Uuid)
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		m.IsServer = c.GetIsServer()
		processed, shouldSend := c.processBeforeSend(m)
		if !shouldSend {
			return nil
		}
		m = processed.(GroupIdentify)
		data, apiMsg, eventUuid, serErr := c.capture.prepare(m)
		if serErr != nil {
			c.notifyFailure([]APIMessage{apiMsg}, serErr)
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg, uuid: eventUuid})
		return

	case Capture:
		if err = validateCaptureEvent(m); err != nil {
			return
		}
		m.Type = "capture"
		m.Uuid = makeUUID(m.Uuid)
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.IsServer = c.GetIsServer()
		captureContext, captureContextErr := resolveCaptureContext(ctx, m.DistinctId, m.Properties, "posthog.Capture")
		if captureContextErr != nil {
			err = captureContextErr
			return
		}
		m.DistinctId = captureContext.distinctID
		m.Properties = captureContext.properties
		if err = m.Validate(); err != nil {
			return
		}
		if m.Flags != nil {
			if m.shouldSendFeatureFlags() {
				c.Warnf("[FEATURE FLAGS] Both Flags and SendFeatureFlags were set on Capture; using Flags and ignoring SendFeatureFlags.")
			}
			// Generated flag properties go down first so user-supplied
			// Properties override them on conflict — matches the Python SDK's
			// merge order so callers can manually overwrite $feature/<key>
			// or $active_feature_flags if they need to.
			m.Properties = m.Flags.eventProperties().Merge(m.Properties)
		} else if m.shouldSendFeatureFlags() && !captureContext.generatedPersonlessDistinctID {
			// Add all feature variants to event. Skip for generated personless IDs so
			// feature flag evaluation always uses a stable caller- or context-supplied ID.
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

			activeFeatureFlags := make([]string, 0, len(featureVariants))
			for feature, variant := range featureVariants {
				propKey := fmt.Sprintf("$feature/%s", feature)
				m.Properties[propKey] = variant
				// $active_feature_flags lists only flags that resolved to a non-false value.
				if variant != false {
					activeFeatureFlags = append(activeFeatureFlags, feature)
				}
			}
			// Sort for deterministic output, matching the snapshot Flags.eventProperties() path.
			sort.Strings(activeFeatureFlags)
			m.Properties["$active_feature_flags"] = activeFeatureFlags
		}
		if m.Properties == nil {
			m.Properties = NewProperties()
		}
		m.Properties.Merge(c.DefaultEventProperties)
		if m.IsServer {
			m.Properties.Set(propertyIsServer, true)
		}
		if captureContext.personlessProcessProfileGuard {
			m.Properties[propertyProcessPersonProfile] = false
		}
		processed, shouldSend := c.processBeforeSend(m)
		if !shouldSend {
			return nil
		}
		m = processed.(Capture)
		// $is_server was materialized into Properties before BeforeSend;
		// from this point, the hook's returned Properties are the source of truth.
		if isServer, ok := m.Properties[propertyIsServer].(bool); ok {
			m.IsServer = isServer
		} else if m.Properties != nil {
			m.IsServer = false
		}
		data, apiMsg, eventUuid, serErr := c.capture.prepare(m)
		if serErr != nil {
			c.notifyFailure([]APIMessage{apiMsg}, serErr)
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg, uuid: eventUuid})
		return

	case Exception:
		m.Type = "exception"
		m.Uuid = makeUUID(m.Uuid)
		m.Timestamp = makeTimestamp(m.Timestamp, ts)
		m.DisableGeoIP = c.GetDisableGeoIP()
		m.IsServer = c.GetIsServer()
		captureContext, captureContextErr := resolveCaptureContext(ctx, m.DistinctId, m.Properties, "posthog.Exception")
		if captureContextErr != nil {
			err = captureContextErr
			return
		}
		m.DistinctId = captureContext.distinctID
		m.Properties = captureContext.properties
		if err = m.Validate(); err != nil {
			return
		}
		processed, shouldSend := c.processBeforeSend(m)
		if !shouldSend {
			return nil
		}
		m = processed.(Exception)
		data, apiMsg, eventUuid, serErr := c.capture.prepare(m)
		if serErr != nil {
			c.notifyFailure([]APIMessage{apiMsg}, serErr)
			return
		}
		sendPrepared(preparedMessage{data: data, msg: apiMsg, uuid: eventUuid})
		return

	default:
		if err = msg.Validate(); err != nil {
			return
		}
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
		c.warnPersonalAPIKeyMissing("ReloadFeatureFlags")
		return ErrNoPersonalAPIKey
	}
	c.featureFlagsPoller.ForceReload()
	return nil
}

func (c *client) GetFeatureFlagPayload(flagConfig FeatureFlagPayload) (string, error) {
	// Non-standard: This method historically left `SendFeatureFlagEvents` as nil.
	// Once `flagConfig.validate()` is called, it sets it to true by default.
	//
	// To preserve historical behavior, we do not coerce to false, deviating from
	// other SDKs that _do not_ send events from getFeatureFlagPayload calls.
	result, err := c.GetFeatureFlagResult(flagConfig)
	if err != nil {
		if errors.Is(err, ErrFlagNotFound) {
			return "", nil
		}
		return "", err
	}
	if result.RawPayload == nil {
		return "", nil
	}
	return *result.RawPayload, nil
}

func (c *client) GetFeatureFlag(flagConfig FeatureFlagPayload) (interface{}, error) {
	result, err := c.GetFeatureFlagResult(flagConfig)
	if err != nil {
		if errors.Is(err, ErrFlagNotFound) {
			return false, nil
		}
		return nil, err
	}
	if result.Variant != nil {
		return *result.Variant, nil
	}
	return result.Enabled, nil
}

func (c *client) GetFeatureFlagResult(flagConfig FeatureFlagPayload) (*FeatureFlagResult, error) {
	return c.getFeatureFlagResultWithContext(context.Background(), flagConfig)
}

func (c *client) getFeatureFlagResultWithContext(ctx context.Context, flagConfig FeatureFlagPayload) (*FeatureFlagResult, error) {
	// Check context before starting
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := flagConfig.validate(); err != nil {
		return nil, err
	}
	if c.featureFlagsPoller == nil && flagConfig.OnlyEvaluateLocally {
		c.warnPersonalAPIKeyMissing("GetFeatureFlagResult")
		return nil, ErrNoPersonalAPIKey
	}

	var flagValue interface{}
	var err error
	// Use stack-allocated evalResult to avoid heap pointer allocation on the hot path
	var evalResult featureFlagEvaluationResult
	// payloadStr and variantStr hold values that will be stored in the result struct
	var payloadStr string
	var variantStr string
	var hasPayload, hasVariant bool
	var locallyEvaluated bool

	if c.featureFlagsPoller != nil {
		// Evaluate flag once to get both value and payload (avoids double evaluation)
		combined := c.featureFlagsPoller.GetFeatureFlagWithPayload(flagConfig)
		flagValue = combined.value
		locallyEvaluated = combined.locallyEvaluated
		err = combined.err
		evalResult.Value = flagValue
		evalResult.Err = err
		if combined.payload != "" {
			payloadStr = combined.payload
			hasPayload = true
		}
		if v, ok := flagValue.(string); ok {
			variantStr = v
			hasVariant = true
		}
	} else {
		// if there's no poller, get the feature flag from the flags endpoint
		c.debugf("getting feature flag from flags endpoint")
		locallyEvaluated = false
		remoteResult := c.getFeatureFlagFromRemote(flagConfig.Key, flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups,
			flagConfig.PersonProperties, flagConfig.GroupProperties)
		evalResult = *remoteResult
		flagValue = evalResult.Value
		err = evalResult.Err
		if f, ok := flagValue.(FlagDetail); ok {
			flagValue = f.GetValue()
			evalResult.Value = flagValue
			ps := rawMessageToString(f.Metadata.Payload)
			if ps != "" {
				payloadStr = ps
				hasPayload = true
			}
			if f.Variant != nil {
				variantStr = *f.Variant
				hasVariant = true
			}
		}
	}

	// Check context after flag evaluation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if *flagConfig.SendFeatureFlagEvents {
		var properties = NewProperties().
			Set("$feature_flag", flagConfig.Key).
			Set("$feature_flag_response", flagValue).
			Set("locally_evaluated", locallyEvaluated)

		if flagConfig.DeviceId != nil {
			properties.Set("$device_id", *flagConfig.DeviceId)
		}

		if evalResult.RequestID != nil {
			properties.Set("$feature_flag_request_id", *evalResult.RequestID)
		}

		if evalResult.EvaluatedAt != nil {
			properties.Set("$feature_flag_evaluated_at", *evalResult.EvaluatedAt)
		}

		if evalResult.FlagDetail != nil {
			properties.Set("$feature_flag_version", evalResult.FlagDetail.Metadata.Version)
			properties.Set("$feature_flag_id", evalResult.FlagDetail.Metadata.ID)
			if evalResult.FlagDetail.Reason != nil {
				properties.Set("$feature_flag_reason", evalResult.FlagDetail.Reason.Description)
			}
		}

		errorString := evalResult.GetErrorString()
		if errorString != "" {
			properties.Set("$feature_flag_error", errorString)
		}

		c.captureFlagCalledIfNeeded(flagConfig.DistinctId, flagConfig.Key, flagValue, flagConfig.DeviceId, properties, flagConfig.Groups)
	}

	if flagValue == nil {
		if evalResult.Err != nil {
			return nil, evalResult.Err
		}
		if evalResult.FlagFailed {
			return nil, fmt.Errorf("%w: '%s' failed to evaluate due to a transient error", ErrFlagNotFound, flagConfig.Key)
		}
		return nil, fmt.Errorf("%w: '%s' does not exist or is disabled", ErrFlagNotFound, flagConfig.Key)
	}

	enabled := false
	switch v := flagValue.(type) {
	case bool:
		enabled = v
	case string:
		enabled = v != ""
	}

	// Build result with embedded string storage so RawPayload/Variant pointers
	// reference fields within the same heap allocation (no separate string escapes).
	result := &FeatureFlagResult{
		Key:          flagConfig.Key,
		Enabled:      enabled,
		payloadStore: payloadStr,
		variantStore: variantStr,
	}
	if hasPayload {
		result.RawPayload = &result.payloadStore
	}
	if hasVariant {
		result.Variant = &result.variantStore
	}
	return result, err
}

// captureFlagCalledIfNeeded fires a $feature_flag_called event if the
// (distinctId, key, value, deviceId, groups) tuple has not already been reported
// on this client. Group context is included so group-scoped flags fire a
// separate event for each group a user is evaluated under. The caller is
// responsible for building the full properties dict; this helper only handles
// dedup and enqueue. It is shared by the legacy per-flag evaluation path and
// the FeatureFlagEvaluations snapshot path so both dedupe identically against
// the same per-distinct_id LRU cache.
func (c *client) captureFlagCalledIfNeeded(distinctId, key string, featureFlagResponse interface{}, deviceId *string, properties Properties, groups Groups) {
	c.captureFlagCalledIfNeededWithContext(context.Background(), distinctId, key, featureFlagResponse, deviceId, properties, groups)
}

func (c *client) captureFlagCalledIfNeededWithContext(ctx context.Context, distinctId, key string, featureFlagResponse interface{}, deviceId *string, properties Properties, groups Groups) {
	deviceIDStr := ""
	if deviceId != nil {
		deviceIDStr = *deviceId
	}
	cacheKey := flagUser{
		distinctID: distinctId,
		flagKey:    key,
		flagValue:  featureFlagResponseCacheKey(featureFlagResponse),
		deviceID:   deviceIDStr,
		groupsRepr: canonicalGroupsRepr(groups),
	}
	if c.distinctIdsFeatureFlagsReported.Contains(cacheKey) {
		return
	}
	if err := c.EnqueueWithContext(ctx, Capture{
		DistinctId: distinctId,
		Event:      "$feature_flag_called",
		Properties: properties,
		Groups:     groups,
	}); err == nil {
		c.distinctIdsFeatureFlagsReported.Add(cacheKey, struct{}{})
	}
}

// featureFlagResponseCacheKey returns a stable type-qualified representation
// of a feature flag response value for exposure deduplication.
func featureFlagResponseCacheKey(value interface{}) string {
	return fmt.Sprintf("%T:%v", value, value)
}

// canonicalGroupsRepr returns a JSON string of the sorted key/value pairs of
// the groups map. Two equal maps that were built with keys inserted in a
// different order produce the same string, so they dedupe to one cache entry.
// Empty / nil groups produce an empty string.
func canonicalGroupsRepr(groups Groups) string {
	if len(groups) == 0 {
		return ""
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]interface{}, len(keys))
	for i, k := range keys {
		pairs[i] = [2]interface{}{k, groups[k]}
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		// Fallback to a stable string concat — the JSON encode failure path
		// shouldn't realistically trigger for a map[string]interface{}, but
		// staying deterministic keeps the dedup behavior consistent.
		var sb strings.Builder
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(fmt.Sprintf("%v", groups[k]))
			sb.WriteString(";")
		}
		return sb.String()
	}
	return string(b)
}

func (c *client) GetRemoteConfigPayload(flagKey string) (string, error) {
	return c.makeRemoteConfigRequest(flagKey)
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
	if c.featureFlagsPoller == nil && flagConfig.OnlyEvaluateLocally {
		c.warnPersonalAPIKeyMissing("GetAllFlags")
		return nil, ErrNoPersonalAPIKey
	}

	var flagsValue map[string]interface{}
	var err error

	if c.featureFlagsPoller != nil {
		// get feature flags from the poller, which uses the personal api key
		flagsValue, err = c.featureFlagsPoller.GetAllFlags(flagConfig)
	} else {
		// if there's no poller, get the feature flags from the flags endpoint
		c.debugf("getting all feature flags from flags endpoint")
		flagsValue, err = c.getAllFeatureFlagsFromRemote(flagConfig.DistinctId, flagConfig.DeviceId, flagConfig.Groups,
			flagConfig.PersonProperties, flagConfig.GroupProperties)
	}

	// Check context after operation
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return flagsValue, err
}

// EvaluateFlagsPayload is the input to Client.EvaluateFlags.
type EvaluateFlagsPayload struct {
	// DistinctId is the user distinct ID to evaluate flags for. It is required
	// unless EvaluateFlagsWithContext can read one from RequestContext.
	DistinctId string
	// DeviceId optionally provides a device_id for remote /flags requests and event deduplication.
	DeviceId *string
	// Groups supplies group identifiers for group-targeted flags.
	Groups Groups
	// PersonProperties overrides person properties used during flag evaluation.
	PersonProperties Properties
	// GroupProperties overrides group properties used during flag evaluation, keyed by group type.
	GroupProperties map[string]Properties
	// OnlyEvaluateLocally prevents fallback to remote /flags requests.
	OnlyEvaluateLocally bool
	// DisableGeoIP, when non-nil, overrides the client-level DisableGeoIP for
	// this evaluation only.
	DisableGeoIP *bool
	// FlagKeys, when non-empty, trims the network call by asking the server
	// to evaluate only the named flags (sent as flag_keys_to_evaluate).
	// This is server-side filtering; use FeatureFlagEvaluations.Only to do
	// client-side filtering of which flags are attached to events from an
	// existing snapshot.
	FlagKeys []string
}

func (c *client) EvaluateFlags(payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	return c.evaluateFlagsWithContext(context.Background(), payload)
}

func (c *client) EvaluateFlagsWithContext(ctx context.Context, payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	return c.evaluateFlagsWithContext(ctx, payload)
}

func (c *client) evaluateFlagsWithContext(ctx context.Context, payload EvaluateFlagsPayload) (*FeatureFlagEvaluations, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if payload.DistinctId == "" {
		if requestContext, ok := RequestContextFromContext(ctx); ok {
			payload.DistinctId = requestContext.DistinctId
		}
	}

	host := c.featureFlagEvaluationsHostWithContext(ctx)

	if payload.DistinctId == "" {
		c.Warnf("EvaluateFlags called without a DistinctId")
		return noopFeatureFlagEvaluations, ErrNoDistinctID
	}

	if payload.Groups == nil {
		payload.Groups = Groups{}
	}
	if payload.PersonProperties == nil {
		payload.PersonProperties = NewProperties()
	}
	if payload.GroupProperties == nil {
		payload.GroupProperties = map[string]Properties{}
	}

	disableGeoIP := c.GetDisableGeoIP()
	if payload.DisableGeoIP != nil {
		disableGeoIP = *payload.DisableGeoIP
	}

	records := make(map[string]evaluatedFlagRecord)
	locallyEvaluated := make(map[string]struct{})
	fallbackToRemote := true

	if c.featureFlagsPoller != nil {
		fallbackToRemote = c.populateLocalEvaluations(records, locallyEvaluated, payload)
	} else if payload.OnlyEvaluateLocally {
		c.warnPersonalAPIKeyMissing("EvaluateFlags")
		return noopFeatureFlagEvaluations, ErrNoPersonalAPIKey
	}

	var requestId string
	var evaluatedAt *int64
	var errorsWhileComputing bool
	var quotaLimited bool

	// remoteErr is returned alongside the snapshot when the /flags request
	// fails, so callers still get any locally-evaluated flags collected above.
	var remoteErr error
	if fallbackToRemote && !payload.OnlyEvaluateLocally {
		flagsResponse, err := c.decider.makeFlagsRequest(
			payload.DistinctId,
			payload.DeviceId,
			payload.Groups,
			payload.PersonProperties,
			payload.GroupProperties,
			disableGeoIP,
			payload.FlagKeys,
		)
		if err != nil {
			remoteErr = err
		} else if flagsResponse != nil {
			requestId = flagsResponse.RequestId
			evaluatedAt = flagsResponse.EvaluatedAt
			errorsWhileComputing = flagsResponse.ErrorsWhileComputingFlags
			quotaLimited = c.isFeatureFlagsQuotaLimited(flagsResponse)
			if !quotaLimited {
				for key, detail := range flagsResponse.Flags {
					if _, alreadyLocal := locallyEvaluated[key]; alreadyLocal {
						continue
					}
					records[key] = recordFromFlagDetail(detail)
				}
			}
		}
	}

	return &FeatureFlagEvaluations{
		host:                 host,
		distinctId:           payload.DistinctId,
		deviceId:             payload.DeviceId,
		groups:               payload.Groups,
		flags:                records,
		requestId:            requestId,
		evaluatedAt:          evaluatedAt,
		errorsWhileComputing: errorsWhileComputing,
		quotaLimited:         quotaLimited,
		accessed:             map[string]struct{}{},
	}, remoteErr
}

// populateLocalEvaluations fills records with locally-resolved flags. It
// returns whether the caller should fall back to a remote /flags request to
// fill in the rest. The local-evaluation loop here mirrors
// FeatureFlagsPoller.GetAllFlags but stores the rich record needed to power
// $feature_flag_called events with locally_evaluated=true.
func (c *client) populateLocalEvaluations(records map[string]evaluatedFlagRecord, locallyEvaluated map[string]struct{}, payload EvaluateFlagsPayload) bool {
	poller := c.featureFlagsPoller
	featureFlags, err := poller.GetFeatureFlags()
	if err != nil {
		return true
	}
	if len(featureFlags) == 0 {
		return true
	}

	flagKeyFilter := map[string]struct{}{}
	for _, k := range payload.FlagKeys {
		flagKeyFilter[k] = struct{}{}
	}

	cohorts := poller.getCohorts()
	fallbackToRemote := false
	const localReason = "Evaluated locally"

	for _, storedFlag := range featureFlags {
		if len(flagKeyFilter) > 0 {
			if _, ok := flagKeyFilter[storedFlag.Key]; !ok {
				continue
			}
		}
		value, err := poller.computeFlagLocally(
			storedFlag,
			payload.DistinctId,
			payload.DeviceId,
			payload.Groups,
			payload.PersonProperties,
			payload.GroupProperties,
			cohorts,
		)
		if err != nil {
			c.debugf("Unable to compute flag '%s' locally - %s", storedFlag.Key, err)
			fallbackToRemote = true
			continue
		}

		record := evaluatedFlagRecord{
			Key:              storedFlag.Key,
			LocallyEvaluated: true,
			Reason:           ptrString(localReason),
		}
		switch v := value.(type) {
		case bool:
			record.Enabled = v
		case string:
			record.Enabled = true
			variant := v
			record.Variant = &variant
		default:
			record.Enabled = false
		}

		if record.Enabled {
			variantKey := "true"
			if record.Variant != nil {
				variantKey = *record.Variant
			}
			if rawPayload, ok := storedFlag.Filters.Payloads[variantKey]; ok {
				if s := rawMessageToString(rawPayload); s != "" {
					payloadStr := s
					record.Payload = &payloadStr
				}
			}
		}

		records[storedFlag.Key] = record
		locallyEvaluated[storedFlag.Key] = struct{}{}
	}

	return fallbackToRemote
}

// recordFromFlagDetail builds an evaluatedFlagRecord from a v4 FlagDetail.
func recordFromFlagDetail(detail FlagDetail) evaluatedFlagRecord {
	record := evaluatedFlagRecord{
		Key:     detail.Key,
		Enabled: detail.Enabled,
		Variant: detail.Variant,
	}
	if detail.Failed != nil && *detail.Failed {
		record.Enabled = false
		errStr := FeatureFlagErrorEvaluationFailed
		record.Error = &errStr
	}
	id := detail.Metadata.ID
	record.ID = &id
	version := detail.Metadata.Version
	record.Version = &version
	if detail.Reason != nil {
		reason := detail.Reason.Description
		record.Reason = &reason
	}
	if s := rawMessageToString(detail.Metadata.Payload); s != "" {
		payloadStr := s
		record.Payload = &payloadStr
	}
	return record
}

func ptrString(s string) *string { return &s }

// featureFlagEvaluationsHostWithContext wires the snapshot's callbacks to this client.
func (c *client) featureFlagEvaluationsHostWithContext(ctx context.Context) featureFlagEvaluationsHost {
	return featureFlagEvaluationsHost{
		captureFlagCalledIfNeeded: func(distinctId, key string, featureFlagResponse interface{}, deviceId *string, properties Properties, groups Groups) {
			c.captureFlagCalledIfNeededWithContext(ctx, distinctId, key, featureFlagResponse, deviceId, properties, groups)
		},
		logger: c.Logger,
	}
}

// Close gracefully shuts down the client, flushing any pending messages.
// If ShutdownTimeout is set to a positive duration, Close waits up to that
// duration for in-flight requests to complete. Otherwise, it waits indefinitely.
// Close is safe to call multiple times; subsequent calls return ErrClosed.
func (c *client) Close() error {
	if c.ShutdownTimeout <= 0 {
		// Zero or negative timeout means wait indefinitely (backward compatible)
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

// processBatch handles a single batch with guaranteed counter decrement.
// It receives the batch from the channel and processes it.
func (c *client) processBatch() {
	// Receive batch from channel - this also "releases" the semaphore slot
	batch, ok := <-c.batches
	if !ok {
		// Channel closed during shutdown
		c.inFlight.Add(-1)
		return
	}

	// CRITICAL: defer ensures counter decrements on ANY exit path
	// (success, JSON error, HTTP error, panic, context cancellation)
	defer c.inFlight.Add(-1)

	// Recover from panics to prevent goroutine death without cleanup
	defer func() {
		if err := recover(); err != nil {
			c.Errorf("panic in batch processor: %v", err)
			c.notifyFailure(batch.msgs, fmt.Errorf("panic: %v", err))
		}
	}()

	c.capture.send(batch)
}

// sendBatch attempts to enqueue a batch for processing.
// Returns true if successful, false if the queue is full after waiting for
// BatchSubmitTimeout (default 100ms). This backpressure smoothing allows
// in-flight requests to complete during transient latency spikes, reducing data loss.
// Set BatchSubmitTimeout to a negative value for non-blocking behavior.
//
// On success, spawns a goroutine to process the batch. The batches channel
// acts as both a queue and concurrency limiter.
func (c *client) sendBatch(batch preparedBatch) bool {
	c.inFlight.Add(1)

	// Negative timeout = non-blocking (immediate drop when queue is full)
	if c.BatchSubmitTimeout < 0 {
		select {
		case c.batches <- batch:
			go c.processBatch()
			return true
		default:
			c.inFlight.Add(-1)
			return false
		}
	}

	// Blocking send with timeout - allows in-flight requests to complete during latency spikes
	timer := time.NewTimer(c.BatchSubmitTimeout)
	select {
	case c.batches <- batch:
		timer.Stop()
		go c.processBatch()
		return true
	case <-timer.C:
		c.inFlight.Add(-1)
		return false
	}
}

// awaitDrain polls until all in-flight batches complete or context times out.
func (c *client) awaitDrain(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for c.inFlight.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
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

		var httpErr *httpError
		if errors.As(err, &httpErr) && !isRetryableStatus(httpErr.statusCode) {
			c.notifyFailure(pb.msgs, err)
			return
		}

		// Check if context is cancelled (shutdown timeout exceeded)
		if c.ctx.Err() != nil {
			c.Errorf("%d messages dropped: shutdown timeout", len(pb.msgs))
			c.notifyFailure(pb.msgs, err)
			return
		}

		retryDelay := c.RetryAfter(i)
		if httpErr != nil && httpErr.hasRetryAfter && httpErr.retryAfter > retryDelay {
			retryDelay = httpErr.retryAfter
		}

		// Wait for retry or shutdown
		retryTimer := time.NewTimer(retryDelay)
		select {
		case <-retryTimer.C:
			// continue to next attempt
		case <-c.quit:
			// Shutdown initiated - stop timer and exit retry loop
			if !retryTimer.Stop() {
				<-retryTimer.C // Drain channel if timer already fired
			}
			c.notifyFailure(pb.msgs, err)
			return
		case <-c.ctx.Done():
			// Context cancelled - stop timer and exit
			if !retryTimer.Stop() {
				<-retryTimer.C // Drain channel if timer already fired
			}
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
	body := b
	encoding := ""

	if c.Compression == CompressionGzip {
		compressed, err := compressGzip(b)
		if err != nil {
			c.Warnf("gzip compression failed; sending uncompressed - %s", err)
		} else {
			url += "?compression=gzip"
			body = compressed
			encoding = "gzip"
		}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		c.Errorf("creating request - %s", err)
		return err
	}

	version := getVersion()

	req.Header.Add("User-Agent", SDKName+"/"+version)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Length", fmt.Sprintf("%d", len(body)))
	if encoding != "" {
		req.Header.Add("Content-Encoding", encoding)
	}

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
	retryAfter, hasRetryAfter := parseRetryAfter(res.Header.Get("Retry-After"), c.now())
	return &httpError{
		statusCode:    res.StatusCode,
		status:        res.Status,
		retryAfter:    retryAfter,
		hasRetryAfter: hasRetryAfter,
	}
}

type httpError struct {
	statusCode    int
	status        string
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%d %s", e.statusCode, e.status)
}

func isRetryableStatus(statusCode int) bool {
	if statusCode >= 500 {
		return true
	}
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}

	if t, err := http.ParseTime(value); err == nil {
		delay := t.Sub(now)
		if delay <= 0 {
			return 0, false
		}
		return delay, true
	}

	return 0, false
}

// loop processes messages from the msgs channel, batches them by size,
// and spawns goroutines to send batches.
func (c *client) loop() {
	defer close(c.batches) // prevent any pending receives from blocking
	defer close(c.shutdown)
	if c.featureFlagsPoller != nil {
		defer c.featureFlagsPoller.shutdownPoller()
	}

	var batchData []json.RawMessage
	var batchMsgs []APIMessage
	var batchUuids []string
	var batchSize int

	tick := time.NewTicker(c.Interval)
	defer tick.Stop()

	// Helper to create and send a batch
	flushBatch := func() bool {
		if len(batchData) == 0 {
			return true
		}
		batch := preparedBatch{data: batchData, msgs: batchMsgs, uuids: batchUuids}
		if !c.sendBatch(batch) {
			c.Errorf("sending batch failed - %s", ErrTooManyRequests)
			c.notifyFailure(batchMsgs, ErrTooManyRequests)
			return false
		}
		return true
	}

	// Helper to reset batch state
	resetBatch := func() {
		batchData, batchMsgs, batchUuids, batchSize = nil, nil, nil, 0
	}

	// Helper to process a single message: validate, batch, flush if needed.
	// Returns false if message was rejected (oversized).
	processMessage := func(prepared preparedMessage) bool {
		msgSize := len(prepared.data)

		if msgSize > maxMessageBytes {
			c.Errorf("message exceeds maximum size (%d > %d)", msgSize, maxMessageBytes)
			c.notifyFailure([]APIMessage{prepared.msg}, ErrMessageTooBig)
			return false
		}

		if batchSize+msgSize > maxBatchBytes && len(batchData) > 0 {
			flushBatch()
			resetBatch()
		}

		batchData = append(batchData, prepared.data)
		batchMsgs = append(batchMsgs, prepared.msg)
		if c.CaptureMode == CaptureModeAnalyticsV1 {
			batchUuids = append(batchUuids, prepared.uuid)
		}
		batchSize += msgSize

		if len(batchData) >= c.BatchSize {
			flushBatch()
			resetBatch()
		}
		return true
	}

	for {
		select {
		case prepared := <-c.msgs:
			if !processMessage(prepared) {
				continue
			}
			c.debugf("buffer (%d/%d, %d bytes) %v", len(batchData), c.BatchSize, batchSize, prepared.msg)

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
				processMessage(prepared)
			}

			// Flush any remaining messages
			if len(batchData) > 0 {
				c.debugf("flushing final batch of %d messages", len(batchData))
				flushBatch()
			}

			// Wait for in-flight batches to complete
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

func (c *client) warnPersonalAPIKeyMissing(method string) {
	c.Warnf("PostHog personal_api_key is not configured; %s requires a PersonalApiKey.", method)
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
		c.warnPersonalAPIKeyMissing("Capture.SendFeatureFlags")
		return nil, ErrNoPersonalAPIKey
	}

	var deviceId *string
	onlyEvaluateLocally := false
	if options != nil {
		deviceId = options.DeviceId
		onlyEvaluateLocally = options.OnlyEvaluateLocally
	}

	// Prefer local evaluation and only fall back to a remote /flags request for flags
	// that can't be computed locally. OnlyEvaluateLocally keeps this strictly local.
	return c.featureFlagsPoller.getFeatureFlagVariantsWithFallback(distinctId, deviceId, groups, personProperties, groupProperties, onlyEvaluateLocally)
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

	if c.PersonalApiKey == "" {
		c.warnPersonalAPIKeyMissing("GetRemoteConfigPayload")
		return "", ErrNoPersonalAPIKey
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

func (c *client) getFeatureFlagFromRemote(key string, distinctId string, deviceId *string, groups Groups, personProperties Properties,
	groupProperties map[string]Properties) *featureFlagEvaluationResult {

	result := &featureFlagEvaluationResult{
		Value: nil,
	}

	flagsResponse, err := c.decider.makeFlagsRequest(distinctId, deviceId, groups, personProperties, groupProperties, c.GetDisableGeoIP(), nil)

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
		// If the flag failed evaluation due to a transient server error,
		// don't return the incorrect enabled=false value
		if flagDetail.Failed != nil && *flagDetail.Failed {
			result.FlagFailed = true
		} else {
			result.Value = flagDetail
			result.FlagDetail = &flagDetail
			result.FlagMissing = false
		}
	} else {
		result.FlagMissing = true
	}

	return result
}

func (c *client) getAllFeatureFlagsFromRemote(distinctId string, deviceId *string, groups Groups, personProperties Properties, groupProperties map[string]Properties) (map[string]interface{}, error) {
	flagsResponse, err := c.decider.makeFlagsRequest(distinctId, deviceId, groups, personProperties, groupProperties, c.GetDisableGeoIP(), nil)
	if err != nil {
		return nil, err
	}

	if c.isFeatureFlagsQuotaLimited(flagsResponse) {
		return emptyFlagValues, nil
	}

	return flagsResponse.FeatureFlags, nil
}
