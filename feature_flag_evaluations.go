package posthog

import (
	"sort"
	"strings"
	"sync"
)

// FeatureFlagEvaluations is a snapshot of feature-flag evaluations for a
// single distinct_id, returned by Client.EvaluateFlags. It is the recommended
// way to evaluate many flags for a single user with a single network request:
// the snapshot can power any number of IsEnabled / GetFlag checks and can be
// attached to a Capture event via Capture.Flags so the event carries the
// exact values the application branched on, with no additional /flags call.
//
// Calls to IsEnabled and GetFlag fire $feature_flag_called (deduped per
// (distinct_id, key, device_id)). GetFlagPayload does not fire an event.
type FeatureFlagEvaluations struct {
	host                    featureFlagEvaluationsHost
	distinctId              string
	deviceId                *string
	groups                  Groups
	flags                   map[string]evaluatedFlagRecord
	requestId               string
	evaluatedAt             *int64
	flagDefinitionsLoadedAt *int64
	// errorsWhileComputing and quotaLimited capture response-level error
	// signals from the /flags request. They are combined with per-flag
	// errors in the $feature_flag_called event so consumers see the same
	// granularity the legacy single-flag path emits.
	errorsWhileComputing bool
	quotaLimited         bool

	mu       sync.Mutex
	accessed map[string]struct{}
}

// evaluatedFlagRecord is the per-flag entry stored in a snapshot. All fields
// are optional except Key and Enabled, mirroring the v4 /flags response shape
// with an additional LocallyEvaluated marker for poller-resolved flags.
type evaluatedFlagRecord struct {
	Key              string
	Enabled          bool
	Variant          *string
	Payload          *string
	ID               *int
	Version          *int
	Reason           *string
	LocallyEvaluated bool
	Error            *string
}

// featureFlagEvaluationsHost is the small callback surface a snapshot uses to
// talk back to the client. It is intentionally narrow so tests can construct
// snapshots without spinning up a full client. Warnings are routed through
// the SDK's Logger; users who want them silenced should pass a Logger that
// drops Warnf calls.
type featureFlagEvaluationsHost struct {
	captureFlagCalledIfNeeded func(distinctId, key string, deviceId *string, properties Properties, groups Groups)
	logger                    Logger
}

// IsEnabled reports whether the flag is enabled for this snapshot's user.
// Unknown flags return false. The first call for a given key fires
// $feature_flag_called with full evaluation metadata; subsequent calls with
// the same response are deduped against the client's per-distinct_id cache.
func (e *FeatureFlagEvaluations) IsEnabled(key string) bool {
	if e == nil {
		return false
	}
	flag, ok := e.flags[key]
	e.recordAccess(key)
	if !ok {
		return false
	}
	return flag.Enabled
}

// GetFlag returns the value of the flag: a string for multivariate flags,
// true or false for boolean flags, or nil for unknown flags. The first call
// for a given key fires $feature_flag_called with full evaluation metadata;
// subsequent calls with the same response are deduped.
func (e *FeatureFlagEvaluations) GetFlag(key string) interface{} {
	if e == nil {
		return nil
	}
	flag, ok := e.flags[key]
	e.recordAccess(key)
	if !ok {
		return nil
	}
	if !flag.Enabled {
		return false
	}
	if flag.Variant != nil {
		return *flag.Variant
	}
	return true
}

// GetFlagPayload returns the raw JSON payload string for the flag, or "" if
// no payload is configured or the flag is unknown. Unlike IsEnabled and
// GetFlag, it does not record an access and does not fire an event.
func (e *FeatureFlagEvaluations) GetFlagPayload(key string) string {
	if e == nil {
		return ""
	}
	flag, ok := e.flags[key]
	if !ok || flag.Payload == nil {
		return ""
	}
	return *flag.Payload
}

// Keys returns the sorted list of evaluated flag keys.
func (e *FeatureFlagEvaluations) Keys() []string {
	if e == nil {
		return nil
	}
	keys := make([]string, 0, len(e.flags))
	for k := range e.flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// OnlyAccessed returns a filtered copy of this snapshot containing only the
// flags that were accessed via IsEnabled or GetFlag before this call.
//
// The method honors its name: if nothing has been accessed yet, the returned
// snapshot is empty. Pre-access the flags you want to attach before calling.
//
// The returned snapshot tracks access independently from the receiver;
// further accesses on the child do not influence what the parent sees.
func (e *FeatureFlagEvaluations) OnlyAccessed() *FeatureFlagEvaluations {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	filtered := make(map[string]evaluatedFlagRecord, len(e.accessed))
	for k := range e.accessed {
		if flag, ok := e.flags[k]; ok {
			filtered[k] = flag
		}
	}
	e.mu.Unlock()
	return e.cloneWith(filtered)
}

// Only returns a filtered copy of this snapshot keeping only the named flags
// that were actually evaluated. Unknown keys are dropped with a warning.
//
// The returned snapshot tracks access independently from the receiver.
func (e *FeatureFlagEvaluations) Only(keys []string) *FeatureFlagEvaluations {
	if e == nil {
		return nil
	}
	filtered := make(map[string]evaluatedFlagRecord, len(keys))
	var missing []string
	for _, k := range keys {
		if flag, ok := e.flags[k]; ok {
			filtered[k] = flag
		} else {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 && e.host.logger != nil {
		e.host.logger.Warnf("FeatureFlagEvaluations.Only() was called with flag keys that are not in the evaluation set and will be dropped: %s", strings.Join(missing, ", "))
	}
	return e.cloneWith(filtered)
}

// eventProperties returns the $feature/<key> and $active_feature_flags
// properties for the snapshot, suitable for merging into a Capture event.
func (e *FeatureFlagEvaluations) eventProperties() Properties {
	props := NewProperties()
	if e == nil || len(e.flags) == 0 {
		return props
	}
	active := make([]string, 0, len(e.flags))
	for key, flag := range e.flags {
		var value interface{}
		switch {
		case !flag.Enabled:
			value = false
		case flag.Variant != nil:
			value = *flag.Variant
		default:
			value = true
		}
		props.Set("$feature/"+key, value)
		if flag.Enabled {
			active = append(active, key)
		}
	}
	sort.Strings(active)
	if len(active) > 0 {
		props.Set("$active_feature_flags", active)
	}
	return props
}

// recordAccess marks key as accessed, builds the $feature_flag_called event
// properties, and forwards them to the client's dedup helper. Access from a
// snapshot bound to an empty distinct_id is silently dropped — those events
// would be useless and would pollute analytics with empty distinct_ids.
func (e *FeatureFlagEvaluations) recordAccess(key string) {
	e.mu.Lock()
	if e.accessed == nil {
		e.accessed = make(map[string]struct{})
	}
	_, alreadyAccessed := e.accessed[key]
	e.accessed[key] = struct{}{}
	e.mu.Unlock()

	if e.distinctId == "" {
		return
	}
	if e.host.captureFlagCalledIfNeeded == nil {
		return
	}

	flag, found := e.flags[key]

	var response interface{}
	switch {
	case !found:
		response = nil
	case !flag.Enabled:
		response = false
	case flag.Variant != nil:
		response = *flag.Variant
	default:
		response = true
	}

	properties := NewProperties().
		Set("$feature_flag", key).
		Set("$feature_flag_response", response).
		Set("$feature/"+key, response).
		Set("locally_evaluated", found && flag.LocallyEvaluated)

	if e.deviceId != nil {
		properties.Set("$device_id", *e.deviceId)
	}
	if e.requestId != "" {
		properties.Set("$feature_flag_request_id", e.requestId)
	}
	if found {
		if flag.Payload != nil {
			properties.Set("$feature_flag_payload", *flag.Payload)
		}
		if flag.ID != nil {
			properties.Set("$feature_flag_id", *flag.ID)
		}
		if flag.Version != nil {
			properties.Set("$feature_flag_version", *flag.Version)
		}
		if flag.Reason != nil {
			properties.Set("$feature_flag_reason", *flag.Reason)
		}
		if flag.LocallyEvaluated {
			if e.flagDefinitionsLoadedAt != nil {
				properties.Set("$feature_flag_definitions_loaded_at", *e.flagDefinitionsLoadedAt)
			}
		} else if e.evaluatedAt != nil {
			properties.Set("$feature_flag_evaluated_at", *e.evaluatedAt)
		}
	}

	// Build the comma-joined $feature_flag_error matching the single-flag
	// path's granularity: response-level errors (errors-while-computing,
	// quota-limited) are combined with per-flag errors so consumers can
	// filter by type.
	var errs []string
	if e.errorsWhileComputing {
		errs = append(errs, FeatureFlagErrorErrorsWhileComputing)
	}
	if e.quotaLimited {
		errs = append(errs, FeatureFlagErrorQuotaLimited)
	}
	if !found {
		errs = append(errs, FeatureFlagErrorFlagMissing)
	} else if flag.Error != nil {
		errs = append(errs, *flag.Error)
	}
	if len(errs) > 0 {
		properties.Set("$feature_flag_error", strings.Join(errs, ","))
	}

	// Dedup is owned by the client cache; alreadyAccessed is a per-snapshot
	// hint that lets us skip the client call when we know we have already
	// fired for this key from this snapshot.
	if alreadyAccessed {
		return
	}
	e.host.captureFlagCalledIfNeeded(e.distinctId, key, e.deviceId, properties, e.groups)
}

// cloneWith builds a child snapshot with the given flag set. The accessed set
// is copied so further access on the child does not leak back into the
// parent's accessed set.
func (e *FeatureFlagEvaluations) cloneWith(flags map[string]evaluatedFlagRecord) *FeatureFlagEvaluations {
	e.mu.Lock()
	accessedCopy := make(map[string]struct{}, len(e.accessed))
	for k := range e.accessed {
		accessedCopy[k] = struct{}{}
	}
	e.mu.Unlock()

	return &FeatureFlagEvaluations{
		host:                    e.host,
		distinctId:              e.distinctId,
		deviceId:                e.deviceId,
		groups:                  e.groups,
		flags:                   flags,
		requestId:               e.requestId,
		evaluatedAt:             e.evaluatedAt,
		flagDefinitionsLoadedAt: e.flagDefinitionsLoadedAt,
		errorsWhileComputing:    e.errorsWhileComputing,
		quotaLimited:            e.quotaLimited,
		accessed:                accessedCopy,
	}
}
