package posthogotel

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/posthog/posthog-go"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	distinctIDAttribute = "posthog.distinct_id"
	sessionIDAttribute  = "$session_id"
)

// ingestion prefers posthog.distinct_id, so adding the context's distinct ID
// would override a user the span already names.
var identityAttributes = []attribute.Key{
	distinctIDAttribute,
	"user.id",
	"ai.telemetry.metadata.posthog_distinct_id",
	"ai.settings.context.posthog_distinct_id",
}

// spanKey identifies a span between OnStart and OnEnd.
type spanKey struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

// requestContextStore holds the request context captured in OnStart for each
// live span.
type requestContextStore struct {
	mu      sync.Mutex
	entries map[spanKey]posthog.RequestContext
	closed  bool
}

func (s *requestContextStore) put(key spanKey, requestContext posthog.RequestContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.entries == nil {
		s.entries = make(map[spanKey]posthog.RequestContext)
	}
	s.entries[key] = requestContext
}

func (s *requestContextStore) take(key spanKey) (posthog.RequestContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestContext, ok := s.entries[key]
	delete(s.entries, key)
	return requestContext, ok
}

// close drops every entry and ignores later puts.
func (s *requestContextStore) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	clear(s.entries)
}

func (s *requestContextStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func keyOf(span interface{ SpanContext() trace.SpanContext }) spanKey {
	sc := span.SpanContext()
	return spanKey{traceID: sc.TraceID(), spanID: sc.SpanID()}
}

// capturedRequestContext returns the PostHog request context carried by ctx,
// or false when it has nothing to export.
func capturedRequestContext(ctx context.Context) (posthog.RequestContext, bool) {
	requestContext, ok := posthog.RequestContextFromContext(ctx)
	if !ok || (requestContext.DistinctId == "" && requestContext.SessionId == "" && len(requestContext.Properties) == 0) {
		return posthog.RequestContext{}, false
	}
	return requestContext, true
}

// requestContextAttributes returns the attributes for a PostHog request
// context, sorted by key. Properties override the distinct ID and session ID.
func requestContextAttributes(requestContext posthog.RequestContext) []attribute.KeyValue {
	values := make(map[string]interface{}, len(requestContext.Properties)+2)
	if requestContext.DistinctId != "" {
		values[distinctIDAttribute] = requestContext.DistinctId
	}
	if requestContext.SessionId != "" {
		values[sessionIDAttribute] = requestContext.SessionId
	}
	for key, value := range requestContext.Properties {
		values[key] = value
	}

	attrs := make([]attribute.KeyValue, 0, len(values))
	for key, value := range values {
		if kv, ok := toAttribute(key, value); ok {
			attrs = append(attrs, kv)
		}
	}
	slices.SortFunc(attrs, func(a, b attribute.KeyValue) int { return strings.Compare(string(a.Key), string(b.Key)) })
	return attrs
}

// spanWithRequestContext is a copy of a span for export: the span's own
// attributes plus the request context attributes it lacks. Live spans and
// other processors never see them.
type spanWithRequestContext struct {
	sdktrace.ReadOnlySpan
	attributes []attribute.KeyValue
}

func (s spanWithRequestContext) Attributes() []attribute.KeyValue { return s.attributes }

// withRequestContext returns span with extra added, keeping any attribute the
// span already has.
func withRequestContext(span sdktrace.ReadOnlySpan, extra []attribute.KeyValue) sdktrace.ReadOnlySpan {
	own := span.Attributes()
	existing := make(map[attribute.Key]struct{}, len(own))
	for _, kv := range own {
		existing[kv.Key] = struct{}{}
	}
	hasIdentity := slices.ContainsFunc(identityAttributes, func(key attribute.Key) bool {
		_, ok := existing[key]
		return ok
	})
	merged := make([]attribute.KeyValue, len(own), len(own)+len(extra))
	copy(merged, own)
	for _, kv := range extra {
		if hasIdentity && kv.Key == distinctIDAttribute {
			continue
		}
		if _, ok := existing[kv.Key]; !ok {
			merged = append(merged, kv)
		}
	}
	return spanWithRequestContext{ReadOnlySpan: span, attributes: merged}
}

// toAttribute converts a property value to an attribute. It skips nil values,
// empty strings, nil slices and non-finite numbers. Slices other than []string,
// []bool, []int, []int64, []float64 and []interface{} holding one primitive
// kind, and all other types OpenTelemetry can't hold,
// become JSON strings, except values that encode as a JSON string, which become
// that string. Values that encode as an empty JSON string are skipped.
func toAttribute(key string, value interface{}) (attribute.KeyValue, bool) {
	switch v := value.(type) {
	case nil:
		return attribute.KeyValue{}, false
	case string:
		if v == "" {
			return attribute.KeyValue{}, false
		}
		return attribute.String(key, v), true
	case bool:
		return attribute.Bool(key, v), true
	case int:
		return attribute.Int(key, v), true
	case int8:
		return attribute.Int64(key, int64(v)), true
	case int16:
		return attribute.Int64(key, int64(v)), true
	case int32:
		return attribute.Int64(key, int64(v)), true
	case int64:
		return attribute.Int64(key, v), true
	case uint8:
		return attribute.Int64(key, int64(v)), true
	case uint16:
		return attribute.Int64(key, int64(v)), true
	case uint32:
		return attribute.Int64(key, int64(v)), true
	case uint:
		if uint64(v) <= math.MaxInt64 {
			return attribute.Int64(key, int64(v)), true
		}
	case uint64:
		if v <= math.MaxInt64 {
			return attribute.Int64(key, int64(v)), true
		}
	case float32:
		// Parsing the shortest float32 form avoids widening noise such as 0.10000000149011612.
		widened, err := strconv.ParseFloat(strconv.FormatFloat(float64(v), 'g', -1, 32), 64)
		if err != nil || !isFinite(widened) {
			return attribute.KeyValue{}, false
		}
		return attribute.Float64(key, widened), true
	case float64:
		if !isFinite(v) {
			return attribute.KeyValue{}, false
		}
		return attribute.Float64(key, v), true
	case []string:
		if v == nil {
			return attribute.KeyValue{}, false
		}
		return attribute.StringSlice(key, v), true
	case []bool:
		if v == nil {
			return attribute.KeyValue{}, false
		}
		return attribute.BoolSlice(key, v), true
	case []int:
		if v == nil {
			return attribute.KeyValue{}, false
		}
		return attribute.IntSlice(key, v), true
	case []int64:
		if v == nil {
			return attribute.KeyValue{}, false
		}
		return attribute.Int64Slice(key, v), true
	case []interface{}:
		if kv, ok := homogeneousSliceAttribute(key, v); ok {
			return kv, true
		}
	case []float64:
		if v == nil || slices.ContainsFunc(v, func(f float64) bool { return !isFinite(f) }) {
			return attribute.KeyValue{}, false
		}
		return attribute.Float64Slice(key, v), true
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return attribute.KeyValue{}, false
	}
	var plain string
	if encoded[0] == '"' && json.Unmarshal(encoded, &plain) == nil {
		if plain == "" {
			return attribute.KeyValue{}, false
		}
		return attribute.String(key, plain), true
	}
	return attribute.String(key, string(encoded)), true
}

// homogeneousSliceAttribute converts a slice whose elements are all strings,
// all bools, all integers or all finite floats into the matching typed slice.
func homogeneousSliceAttribute(key string, values []interface{}) (attribute.KeyValue, bool) {
	if len(values) == 0 {
		return attribute.KeyValue{}, false
	}
	switch values[0].(type) {
	case string:
		return convertSlice(values, func(v interface{}) (string, bool) { s, ok := v.(string); return s, ok }, attribute.StringSlice, key)
	case bool:
		return convertSlice(values, func(v interface{}) (bool, bool) { b, ok := v.(bool); return b, ok }, attribute.BoolSlice, key)
	case int, int8, int16, int32, int64, uint8, uint16, uint32:
		return convertSlice(values, integerValue, attribute.Int64Slice, key)
	case float32, float64:
		return convertSlice(values, finiteFloatValue, attribute.Float64Slice, key)
	}
	return attribute.KeyValue{}, false
}

func convertSlice[T any](values []interface{}, convert func(interface{}) (T, bool), build func(string, []T) attribute.KeyValue, key string) (attribute.KeyValue, bool) {
	converted := make([]T, len(values))
	for i, value := range values {
		item, ok := convert(value)
		if !ok {
			return attribute.KeyValue{}, false
		}
		converted[i] = item
	}
	return build(key, converted), true
}

func integerValue(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	}
	return 0, false
}

func finiteFloatValue(value interface{}) (float64, bool) {
	var f float64
	switch v := value.(type) {
	case float32:
		parsed, err := strconv.ParseFloat(strconv.FormatFloat(float64(v), 'g', -1, 32), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	case float64:
		f = v
	default:
		return 0, false
	}
	return f, isFinite(f)
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
