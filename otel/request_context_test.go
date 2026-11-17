package posthogotel

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/posthog/posthog-go/v2"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// requestContextHarness wires the PostHog processor and a trailing recorder
// onto one provider, so tests can compare what each of them sees.
type requestContextHarness struct {
	t         *testing.T
	server    *otlpServer
	provider  *sdktrace.TracerProvider
	other     *tracetest.SpanRecorder
	processor *SpanProcessor
}

func newRequestContextHarness(t *testing.T) *requestContextHarness {
	t.Helper()
	server := newOTLPServer(t)
	processor, err := NewSpanProcessor(context.Background(), "phc_test", WithHost(server.server.URL))
	if err != nil {
		t.Fatalf("NewSpanProcessor: %v", err)
	}
	other := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor), sdktrace.WithSpanProcessor(other))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return &requestContextHarness{t: t, server: server, provider: provider, other: other, processor: processor}
}

func (h *requestContextHarness) emitAISpan(ctx context.Context, attrs ...attribute.KeyValue) {
	h.emitSpan(ctx, "gen_ai.chat", attrs...)
}

func (h *requestContextHarness) emitSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	_, span := h.provider.Tracer("test").Start(ctx, name, trace.WithAttributes(attrs...))
	span.End()
}

func (h *requestContextHarness) exported() []exportedSpan {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.provider.ForceFlush(ctx); err != nil {
		h.t.Fatalf("ForceFlush: %v", err)
	}
	return h.server.exportedSpans()
}

func (h *requestContextHarness) capturedCount() int {
	return h.processor.requestContexts.len()
}

func TestSpanProcessorExportsRequestContextPerContext(t *testing.T) {
	h := newRequestContextHarness(t)
	alice := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		DistinctId: "alice", SessionId: "s_alice", Properties: posthog.Properties{"plan": "pro"},
	})
	bob := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		DistinctId: "bob", SessionId: "s_bob", Properties: posthog.Properties{"plan": "free"},
	})

	h.emitAISpan(alice)
	h.emitAISpan(bob)

	exported := h.exported()
	if len(exported) != 2 {
		t.Fatalf("exported %d spans, want 2", len(exported))
	}
	for i, want := range []struct{ user, session, plan string }{{"alice", "s_alice", "pro"}, {"bob", "s_bob", "free"}} {
		attrs := exported[i].attributes
		for key, wantValue := range map[string]string{"posthog.distinct_id": want.user, "$session_id": want.session, "plan": want.plan} {
			if got := attrs[key].GetStringValue(); got != wantValue {
				t.Errorf("span %d %s = %q, want %q", i, key, got, wantValue)
			}
		}
	}
}

func TestSpanProcessorKeepsContextOffLiveSpansAndOtherProcessors(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		DistinctId: "alice", Properties: posthog.Properties{"plan": "pro"},
	})

	h.emitAISpan(ctx, attribute.String("gen_ai.system", "openai"))

	if got := h.exported()[0].attributes["posthog.distinct_id"].GetStringValue(); got != "alice" {
		t.Fatalf("exported posthog.distinct_id = %q, want alice", got)
	}
	seen := h.other.Ended()[0].Attributes()
	if len(seen) != 1 || seen[0].Key != "gen_ai.system" {
		t.Errorf("other processor saw attributes %v, want only gen_ai.system", seen)
	}
}

func TestSpanProcessorKeepsSpanAttributesOverContext(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		DistinctId: "alice", SessionId: "s_alice",
	})

	h.emitAISpan(ctx, attribute.String("$session_id", "s_span"))

	attrs := h.exported()[0].attributes
	if got := attrs["$session_id"].GetStringValue(); got != "s_span" {
		t.Errorf("$session_id = %q, want s_span", got)
	}
	if got := attrs["posthog.distinct_id"].GetStringValue(); got != "alice" {
		t.Errorf("posthog.distinct_id = %q, want alice", got)
	}
}

func TestSpanProcessorLetsPropertiesOverrideContextIDs(t *testing.T) {
	h := newRequestContextHarness(t)
	noSession := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		Properties: posthog.Properties{"$session_id": "s_prop"},
	})
	withSession := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		SessionId: "s_alice", Properties: posthog.Properties{"$session_id": "s_prop"},
	})

	h.emitAISpan(noSession)
	h.emitAISpan(withSession)

	for i, span := range h.exported() {
		if got := span.attributes["$session_id"].GetStringValue(); got != "s_prop" {
			t.Errorf("span %d $session_id = %q, want s_prop", i, got)
		}
	}
}

func TestSpanProcessorPassesExplicitAISessionIDPropertyThrough(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		SessionId: "s_alice", Properties: posthog.Properties{"$ai_session_id": "conv_42"},
	})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	if got := attrs["$ai_session_id"].GetStringValue(); got != "conv_42" {
		t.Errorf("$ai_session_id = %q, want conv_42", got)
	}
	if got := attrs["$session_id"].GetStringValue(); got != "s_alice" {
		t.Errorf("$session_id = %q, want s_alice", got)
	}
}

func TestSpanProcessorDoesNotDeriveAISessionIDFromContext(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{SessionId: "s_alice"})

	h.emitAISpan(ctx)

	if _, present := h.exported()[0].attributes["$ai_session_id"]; present {
		t.Error("$ai_session_id should not be derived from the context session")
	}
}

func TestSpanProcessorDropsDistinctIDPropertyWhenSpanHasIdentity(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		Properties: posthog.Properties{"posthog.distinct_id": "alice"},
	})

	h.emitAISpan(ctx, attribute.String("user.id", "bob"))

	if _, present := h.exported()[0].attributes["posthog.distinct_id"]; present {
		t.Error("posthog.distinct_id property should not be exported next to user.id")
	}
}

func TestSpanProcessorSkipsContextDistinctIDWhenSpanHasIdentity(t *testing.T) {
	for _, key := range []string{
		"posthog.distinct_id",
		"user.id",
		"ai.telemetry.metadata.posthog_distinct_id",
		"ai.settings.context.posthog_distinct_id",
	} {
		t.Run(key, func(t *testing.T) {
			h := newRequestContextHarness(t)
			ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
				DistinctId: "alice", SessionId: "s_alice",
			})

			h.emitAISpan(ctx, attribute.String(key, "bob"))

			attrs := h.exported()[0].attributes
			if key != "posthog.distinct_id" {
				if _, present := attrs["posthog.distinct_id"]; present {
					t.Errorf("posthog.distinct_id = %q, want none next to %s", attrs["posthog.distinct_id"].GetStringValue(), key)
				}
			} else if got := attrs[key].GetStringValue(); got != "bob" {
				t.Errorf("%s = %q, want bob", key, got)
			}
			if got := attrs["$session_id"].GetStringValue(); got != "s_alice" {
				t.Errorf("$session_id = %q, want s_alice", got)
			}
		})
	}
}

func TestSpanProcessorEncodesStructuredPropertiesAsJSON(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"account": map[string]interface{}{"tier": "pro", "seats": 3},
		"arms":    []string{"short-prompt", "new-router"},
	}})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	if got, want := attrs["account"].GetStringValue(), `{"seats":3,"tier":"pro"}`; got != want {
		t.Errorf("account = %q, want %q", got, want)
	}
	var arms []string
	for _, item := range attrs["arms"].GetArrayValue().GetValues() {
		arms = append(arms, item.GetStringValue())
	}
	if len(arms) != 2 || arms[0] != "short-prompt" || arms[1] != "new-router" {
		t.Errorf("arms = %v, want [short-prompt new-router]", arms)
	}
}

func TestSpanProcessorAddsNothingWithoutRequestContext(t *testing.T) {
	h := newRequestContextHarness(t)

	h.emitAISpan(context.Background())

	if attrs := h.exported()[0].attributes; len(attrs) != 0 {
		t.Errorf("attributes = %v, want none", attrs)
	}
	if h.capturedCount() != 0 {
		t.Errorf("captured entries = %d, want 0", h.capturedCount())
	}
}

func TestSpanProcessorMapsNumericTypesAndSkipsTypedNils(t *testing.T) {
	h := newRequestContextHarness(t)
	var nilPointer *struct{}
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"small":   int32(3),
		"count":   uint(4),
		"huge":    uint64(math.MaxUint64),
		"ratio":   float32(0.1),
		"nothing": nilPointer,
	}})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	for key, want := range map[string]int64{"small": 3, "count": 4} {
		if _, ok := attrs[key].GetValue().(*commonpb.AnyValue_IntValue); !ok || attrs[key].GetIntValue() != want {
			t.Errorf("%s = %v, want int %d", key, attrs[key], want)
		}
	}
	if _, ok := attrs["ratio"].GetValue().(*commonpb.AnyValue_DoubleValue); !ok || attrs["ratio"].GetDoubleValue() != 0.1 {
		t.Errorf("ratio = %v, want double 0.1", attrs["ratio"])
	}
	if _, ok := attrs["huge"].GetValue().(*commonpb.AnyValue_StringValue); !ok {
		t.Errorf("huge = %v, want string fallback", attrs["huge"])
	}
	if _, present := attrs["nothing"]; present {
		t.Error("typed nil pointer should be skipped")
	}
}

func TestSpanProcessorDropsNonAISpanInContextWithoutLeaking(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "alice"})

	h.emitSpan(ctx, "http.request")

	if exported := h.exported(); len(exported) != 0 {
		t.Errorf("exported %v, want no spans", exported)
	}
	if h.capturedCount() != 0 {
		t.Errorf("captured entries = %d, want 0", h.capturedCount())
	}
}

func TestSpanProcessorReleasesCapturedContextOnEnd(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "alice"})

	_, span := h.provider.Tracer("test").Start(ctx, "gen_ai.chat")
	if h.capturedCount() != 1 {
		t.Fatalf("captured entries while span is live = %d, want 1", h.capturedCount())
	}
	span.End()

	if h.capturedCount() != 0 {
		t.Errorf("captured entries after end = %d, want 0", h.capturedCount())
	}
}

func TestSpanProcessorKeepsConcurrentContextsApart(t *testing.T) {
	h := newRequestContextHarness(t)
	const workers = 50

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: fmt.Sprintf("user_%d", i)})
			h.emitSpan(ctx, fmt.Sprintf("gen_ai.chat.%d", i))
		}(i)
	}
	wg.Wait()

	exported := h.exported()
	if len(exported) != workers {
		t.Fatalf("exported %d spans, want %d", len(exported), workers)
	}
	for _, span := range exported {
		var index int
		if _, err := fmt.Sscanf(span.name, "gen_ai.chat.%d", &index); err != nil {
			t.Fatalf("unexpected span name %q", span.name)
		}
		if got, want := span.attributes["posthog.distinct_id"].GetStringValue(), fmt.Sprintf("user_%d", index); got != want {
			t.Errorf("%s posthog.distinct_id = %q, want %q", span.name, got, want)
		}
	}
	if h.capturedCount() != 0 {
		t.Errorf("captured entries = %d, want 0", h.capturedCount())
	}
}

func TestSpanProcessorSkipsNonFiniteNumbers(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"nan":     math.NaN(),
		"inf":     math.Inf(1),
		"neg_inf": float32(math.Inf(-1)),
		"series":  []float64{1, math.NaN()},
		"ok":      2.5,
	}})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	for _, key := range []string{"nan", "inf", "neg_inf", "series"} {
		if _, present := attrs[key]; present {
			t.Errorf("%s should be skipped", key)
		}
	}
	if got := attrs["ok"].GetDoubleValue(); got != 2.5 {
		t.Errorf("ok = %v, want 2.5", got)
	}
}

func TestSpanProcessorExportsStringLikeValuesAsPlainStrings(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"signed_up": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	if got, want := attrs["signed_up"].GetStringValue(), "2026-01-02T03:04:05Z"; got != want {
		t.Errorf("signed_up = %q, want %q", got, want)
	}
}

type emptyLabel struct{}

func (emptyLabel) MarshalJSON() ([]byte, error) { return []byte(`""`), nil }

func TestSpanProcessorSkipsValuesThatEncodeAsEmptyString(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: posthog.Properties{
		"label": emptyLabel{},
		"plan":  "pro",
	}})

	h.emitAISpan(ctx)

	attrs := h.exported()[0].attributes
	if _, ok := attrs["label"]; ok {
		t.Errorf("label exported, want skipped")
	}
	if got := attrs["plan"].GetStringValue(); got != "pro" {
		t.Errorf("plan = %q, want pro", got)
	}
}

func TestSpanProcessorShutdownClearsCapturedContexts(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "alice"})
	h.provider.Tracer("test").Start(ctx, "gen_ai.chat")
	if h.capturedCount() != 1 {
		t.Fatalf("captured entries while span is live = %d, want 1", h.capturedCount())
	}

	if err := h.processor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if h.capturedCount() != 0 {
		t.Errorf("captured entries after shutdown = %d, want 0", h.capturedCount())
	}
}

func TestSpanProcessorExportsSpanWhoseStartWasNeverSeen(t *testing.T) {
	h := newRequestContextHarness(t)
	stub := tracetest.SpanStub{
		Name:        "gen_ai.chat",
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled}),
		Attributes:  []attribute.KeyValue{attribute.String("gen_ai.system", "openai")},
	}

	h.processor.OnEnd(stub.Snapshot())

	exported := h.exported()
	if len(exported) != 1 || len(exported[0].attributes) != 1 || exported[0].attributes["gen_ai.system"].GetStringValue() != "openai" {
		t.Errorf("exported %v, want the span unchanged", exported)
	}
}

func TestSpanProcessorKeepsSpanAttributeWhenPropertyHasAnotherType(t *testing.T) {
	h := newRequestContextHarness(t)
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{
		Properties: posthog.Properties{"seats": "three"},
	})

	h.emitAISpan(ctx, attribute.Int("seats", 3))

	if got := h.exported()[0].attributes["seats"].GetIntValue(); got != 3 {
		t.Errorf("seats = %v, want span's int 3", h.exported()[0].attributes["seats"])
	}
}

func TestSpanProcessorIgnoresContextsCapturedAfterShutdown(t *testing.T) {
	h := newRequestContextHarness(t)
	if err := h.processor.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "alice"})

	h.provider.Tracer("test").Start(ctx, "gen_ai.chat")

	if h.capturedCount() != 0 {
		t.Errorf("captured entries after shutdown = %d, want 0", h.capturedCount())
	}
}

func TestToAttributeSkipsTypedNilSlicesAndKeepsEmptyOnes(t *testing.T) {
	skipped := map[string]interface{}{
		"strings": []string(nil),
		"bools":   []bool(nil),
		"ints":    []int(nil),
		"int64s":  []int64(nil),
		"floats":  []float64(nil),
		"any":     []interface{}(nil),
	}
	for name, value := range skipped {
		if kv, ok := toAttribute(name, value); ok {
			t.Errorf("%s: got %v, want skipped", name, kv)
		}
	}
	kept := map[string]interface{}{
		"strings": []string{},
		"bools":   []bool{},
		"ints":    []int{},
		"int64s":  []int64{},
		"floats":  []float64{},
	}
	for name, value := range kept {
		if _, ok := toAttribute(name, value); !ok {
			t.Errorf("%s: empty slice skipped, want kept", name)
		}
	}
}

func TestToAttributeConvertsHomogeneousInterfaceSlices(t *testing.T) {
	tests := []struct {
		name  string
		value []interface{}
		want  attribute.Value
	}{
		{"strings", []interface{}{"a", "b"}, attribute.StringSliceValue([]string{"a", "b"})},
		{"bools", []interface{}{true, false}, attribute.BoolSliceValue([]bool{true, false})},
		{"ints", []interface{}{1, int32(2), int64(3)}, attribute.Int64SliceValue([]int64{1, 2, 3})},
		{"floats", []interface{}{1.5, float32(2.5)}, attribute.Float64SliceValue([]float64{1.5, 2.5})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv, ok := toAttribute("k", tt.value)
			if !ok || kv.Value != tt.want {
				t.Errorf("got %v (ok=%v), want %v", kv.Value, ok, tt.want)
			}
		})
	}
}

func TestToAttributeEncodesOtherInterfaceSlicesAsJSON(t *testing.T) {
	tests := []struct {
		name  string
		value []interface{}
		want  string
	}{
		{"mixed kinds", []interface{}{"a", 1}, `["a",1]`},
		{"ints and floats", []interface{}{1, 2.5}, `[1,2.5]`},
		{"nested", []interface{}{map[string]interface{}{"a": 1}}, `[{"a":1}]`},
		{"with nil", []interface{}{"a", nil}, `["a",null]`},
		{"empty", []interface{}{}, `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv, ok := toAttribute("k", tt.value)
			if !ok || kv.Value.Type() != attribute.STRING || kv.Value.AsString() != tt.want {
				t.Errorf("got %v (ok=%v), want JSON %s", kv.Value, ok, tt.want)
			}
		})
	}
	if _, ok := toAttribute("k", []interface{}{1.5, math.NaN()}); ok {
		t.Error("slice with a non-finite float should be skipped")
	}
}
