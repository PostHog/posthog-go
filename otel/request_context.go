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
	sessionIDAttribute  = "$ai_session_id"
)

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
}

func (s *requestContextStore) put(key spanKey, requestContext posthog.RequestContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *requestContextStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	merged := make([]attribute.KeyValue, len(own), len(own)+len(extra))
	copy(merged, own)
	for _, kv := range extra {
		if _, ok := existing[kv.Key]; !ok {
			merged = append(merged, kv)
		}
	}
	return spanWithRequestContext{ReadOnlySpan: span, attributes: merged}
}

// toAttribute converts a property value to an attribute. It skips nil values,
// empty strings and non-finite numbers. Slices other than []string, []bool,
// []int, []int64 and []float64, and all other types OpenTelemetry can't hold,
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
		return attribute.StringSlice(key, v), true
	case []bool:
		return attribute.BoolSlice(key, v), true
	case []int:
		return attribute.IntSlice(key, v), true
	case []int64:
		return attribute.Int64Slice(key, v), true
	case []float64:
		if slices.ContainsFunc(v, func(f float64) bool { return !isFinite(f) }) {
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

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
