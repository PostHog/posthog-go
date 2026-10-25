package posthogotel

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/posthog/posthog-go/v2"
	"go.opentelemetry.io/otel/trace"
)

type unreadableSpan struct{ trace.Span }

func (unreadableSpan) SpanContext() trace.SpanContext { panic("example trace lookup failure") }

func TestEnqueueWithContextExceptionTrace(t *testing.T) {
	traceID := trace.TraceID{15: 3}
	spanID := trace.SpanID{7: 4}
	valid := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})
	tests := []struct {
		name              string
		span              trace.SpanContext
		eventProperties   posthog.Properties
		requestProperties posthog.Properties
		wantTrace         interface{}
		wantSpan          interface{}
	}{
		{name: "unsampled leading zeros", span: valid, wantTrace: traceID.String(), wantSpan: spanID.String()},
		{name: "sampled", span: valid.WithTraceFlags(trace.FlagsSampled), wantTrace: traceID.String(), wantSpan: spanID.String()},
		{name: "no span"},
		{name: "zero trace", span: trace.NewSpanContext(trace.SpanContextConfig{SpanID: spanID})},
		{name: "zero span", span: trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID})},
		{name: "event trace", span: valid, eventProperties: posthog.Properties{"$trace_id": "event-trace"}, wantTrace: "event-trace"},
		{name: "event span", span: valid, eventProperties: posthog.Properties{"$span_id": "event-span"}, wantSpan: "event-span"},
		{name: "request trace", span: valid, requestProperties: posthog.Properties{"$trace_id": "request-trace"}, wantTrace: "request-trace"},
		{name: "request span", span: valid, requestProperties: posthog.Properties{"$span_id": "request-span"}, wantSpan: "request-span"},
		{name: "event overrides request", span: valid, eventProperties: posthog.Properties{"$trace_id": "event-trace"}, requestProperties: posthog.Properties{"$trace_id": "request-trace"}, wantTrace: "event-trace"},
		{name: "explicit empty values", span: valid, eventProperties: posthog.Properties{"$trace_id": "", "$span_id": nil}, wantTrace: ""},
		{name: "invalid span keeps caller IDs", eventProperties: posthog.Properties{"$trace_id": "event-trace", "$span_id": "event-span"}, wantTrace: "event-trace", wantSpan: "event-span"},
	}
	for _, tt := range tests {
		for _, pointer := range []bool{false, true} {
			name := tt.name + "/value"
			if pointer {
				name = tt.name + "/pointer"
			}
			t.Run(name, func(t *testing.T) {
				var captured []posthog.Exception
				client, err := posthog.NewWithConfig("example-key", posthog.Config{BeforeSend: func(msg posthog.Message) posthog.Message {
					captured = append(captured, msg.(posthog.Exception))
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := client.Close(); err != nil {
						t.Error(err)
					}
				})
				base := posthog.WithRequestContext(context.Background(), posthog.RequestContext{DistinctId: "example-user", Properties: tt.requestProperties})
				ctx := trace.ContextWithSpanContext(base, tt.span)
				original := posthog.Properties{"example": "kept"}
				for key, value := range tt.eventProperties {
					original[key] = value
				}
				exception := posthog.Exception{Properties: original, ExceptionList: []posthog.ExceptionItem{{Type: "ExampleError", Value: "Example failure"}}}
				var msg posthog.Message = exception
				if pointer {
					msg = &exception
				}
				if err := EnqueueWithContext(ctx, client, msg); err != nil {
					t.Fatal(err)
				}
				if len(captured) != 1 {
					t.Fatalf("captured %d events, want 1", len(captured))
				}
				got := captured[0]
				if got.DistinctId != "example-user" || got.Properties["example"] != "kept" || got.ExceptionList[0].Value != "Example failure" {
					t.Fatalf("exception changed: %+v", got)
				}
				for key, want := range (posthog.Properties{"$trace_id": tt.wantTrace, "$span_id": tt.wantSpan}) {
					value, exists := got.Properties[key]
					_, explicit := tt.eventProperties[key]
					if !reflect.DeepEqual(value, want) || exists != (want != nil || explicit) {
						t.Errorf("%s = %#v (%v), want %#v", key, value, exists, want)
					}
				}
				expected := posthog.Properties{"example": "kept"}
				for key, value := range tt.eventProperties {
					expected[key] = value
				}
				if !reflect.DeepEqual(original, expected) || !reflect.DeepEqual(exception.Properties, expected) {
					t.Fatalf("source properties changed: %#v", original)
				}
				if err := EnqueueWithContext(base, client, msg); err != nil {
					t.Fatal(err)
				}
				if len(captured) != 2 {
					t.Fatalf("captured %d events, want 2", len(captured))
				}
				for _, key := range []string{"$trace_id", "$span_id"} {
					_, fromEvent := tt.eventProperties[key]
					_, fromRequest := tt.requestProperties[key]
					if _, exists := captured[1].Properties[key]; exists != (fromEvent || fromRequest) {
						t.Errorf("%s leaked into exception outside span", key)
					}
				}
			})
		}
	}
}

type enqueueRecorder struct {
	message posthog.Message
	err     error
}

func (c *enqueueRecorder) Enqueue(msg posthog.Message) error { c.message = msg; return c.err }

func TestEnqueueWithContextDelegates(t *testing.T) {
	failure := errors.New("example enqueue failure")
	var nilException *posthog.Exception
	tests := []struct {
		name string
		ctx  context.Context
		msg  posthog.Message
	}{
		{"unreadable span", trace.ContextWithSpan(context.Background(), unreadableSpan{}), posthog.Exception{}},
		{"nil context", nil, posthog.Exception{}},
		{"nil exception", context.Background(), nilException},
		{"nil message", context.Background(), nil},
		{"capture", trace.ContextWithSpan(context.Background(), unreadableSpan{}), posthog.Capture{Event: "example-event"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &enqueueRecorder{err: failure}
			if err := EnqueueWithContext(tt.ctx, client, tt.msg); !errors.Is(err, failure) {
				t.Fatalf("error = %v, want client error", err)
			}
			if !reflect.DeepEqual(client.message, tt.msg) {
				t.Fatalf("message changed: %#v", client.message)
			}
		})
	}
	if err := EnqueueWithContext(context.Background(), nil, posthog.Exception{}); err == nil {
		t.Fatal("nil client must return an error")
	}
}

func TestEnqueueWithContextRequestTraceOverridesForEnqueueClient(t *testing.T) {
	span := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{15: 3}, SpanID: trace.SpanID{7: 4}})
	tests := []struct {
		name              string
		eventProperties   posthog.Properties
		requestProperties posthog.Properties
		wantProperties    posthog.Properties
	}{
		{name: "request trace", requestProperties: posthog.Properties{"$trace_id": "request-trace"}, wantProperties: posthog.Properties{"$trace_id": "request-trace"}},
		{name: "request span", requestProperties: posthog.Properties{"$span_id": "request-span"}, wantProperties: posthog.Properties{"$span_id": "request-span"}},
		{name: "no caller IDs", wantProperties: posthog.Properties{"$trace_id": "00000000000000000000000000000003", "$span_id": "0000000000000004"}},
		{name: "event trace wins", eventProperties: posthog.Properties{"$trace_id": "event-trace"}, requestProperties: posthog.Properties{"$trace_id": "request-trace", "$span_id": "request-span"}, wantProperties: posthog.Properties{"$trace_id": "event-trace", "$span_id": "request-span"}},
		{name: "event span wins", eventProperties: posthog.Properties{"$span_id": "event-span"}, requestProperties: posthog.Properties{"$trace_id": "request-trace", "$span_id": "request-span"}, wantProperties: posthog.Properties{"$trace_id": "request-trace", "$span_id": "event-span"}},
		{name: "empty request values", requestProperties: posthog.Properties{"$trace_id": "", "$span_id": nil}, wantProperties: posthog.Properties{"$trace_id": "", "$span_id": nil}},
		{name: "empty event values", eventProperties: posthog.Properties{"$trace_id": "", "$span_id": nil}, requestProperties: posthog.Properties{"$trace_id": "request-trace", "$span_id": "request-span"}, wantProperties: posthog.Properties{"$trace_id": "", "$span_id": nil}},
	}
	for _, tt := range tests {
		for _, pointer := range []bool{false, true} {
			name := tt.name + "/value"
			if pointer {
				name = tt.name + "/pointer"
			}
			t.Run(name, func(t *testing.T) {
				requestProperties := posthog.Properties{"request-only": "excluded"}
				for key, value := range tt.requestProperties {
					requestProperties[key] = value
				}
				ctx := posthog.WithRequestContext(context.Background(), posthog.RequestContext{Properties: requestProperties})
				ctx = trace.ContextWithSpanContext(ctx, span)
				exception := posthog.Exception{Properties: tt.eventProperties}
				var msg posthog.Message = exception
				if pointer {
					msg = &exception
				}
				client := &enqueueRecorder{}
				if err := EnqueueWithContext(ctx, client, msg); err != nil {
					t.Fatal(err)
				}
				var got posthog.Properties
				switch captured := client.message.(type) {
				case posthog.Exception:
					got = captured.Properties
				case *posthog.Exception:
					got = captured.Properties
				default:
					t.Fatalf("unexpected message: %T", client.message)
				}
				if !reflect.DeepEqual(got, tt.wantProperties) {
					t.Fatalf("properties = %#v, want %#v", got, tt.wantProperties)
				}
			})
		}
	}
}
