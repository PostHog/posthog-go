package posthog

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

func createLogRecord(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.Record{Time: time.Now(), Level: level, Message: msg}
	for _, a := range attrs {
		r.AddAttrs(a)
	}
	return r
}

func TestSlogCaptureHandler_Handle(t *testing.T) {
	optFactory := func(distinctID string) []SlogOption {
		return []SlogOption{
			WithMinCaptureLevel(slog.LevelWarn),
			WithDistinctIDFn(func(_ context.Context, _ slog.Record) string { return distinctID }),
		}
	}

	tests := map[string]struct {
		createLogHandler   func(slog.Handler, EnqueueClient) *SlogCaptureHandler
		record             slog.Record
		nextHandlerEnabled bool
		shouldForward      bool
		shouldEnqueue      bool
	}{
		"nil client: forwards only": {
			createLogHandler: func(next slog.Handler, _ EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, nil, optFactory("test")...)
			},
			record: createLogRecord(slog.LevelError, "title",
				slog.Any("error", errors.New("test-error"))),
			nextHandlerEnabled: true,
			shouldForward:      true,
			shouldEnqueue:      false,
		},
		"below min level: forward only": {
			createLogHandler: func(next slog.Handler, client EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, client, optFactory("test")...)
			},
			record:             createLogRecord(slog.LevelInfo, "warn"),
			nextHandlerEnabled: true,
			shouldForward:      true,
			shouldEnqueue:      false,
		},
		"at min level: forward and enqueue": {
			createLogHandler: func(next slog.Handler, client EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, client, optFactory("test")...)
			},
			record:             createLogRecord(slog.LevelWarn, "warn"),
			nextHandlerEnabled: true,
			shouldForward:      true,
			shouldEnqueue:      true,
		},
		"empty distinct ID: forward only": {
			createLogHandler: func(next slog.Handler, client EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, client, optFactory("")...)
			},
			record:             createLogRecord(slog.LevelError, "oops"),
			nextHandlerEnabled: true,
			shouldForward:      true,
			shouldEnqueue:      false,
		},
		"next handler not enabled: enqueue only": {
			createLogHandler: func(next slog.Handler, client EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, client, optFactory("test")...)
			},
			record:             createLogRecord(slog.LevelError, "title", slog.Any("error", errors.New("kaboom"))),
			nextHandlerEnabled: false,
			shouldForward:      false,
			shouldEnqueue:      true,
		},
		"happy path: forward and enqueues": {
			createLogHandler: func(next slog.Handler, client EnqueueClient) *SlogCaptureHandler {
				return NewSlogCaptureHandler(next, client, optFactory("test")...)
			},
			record:             createLogRecord(slog.LevelError, "title", slog.Any("error", errors.New("kaboom"))),
			nextHandlerEnabled: true,
			shouldForward:      true,
			shouldEnqueue:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			next := &fakeNextSlogHandler{isEnabled: tc.nextHandlerEnabled}
			client := &fakeEnqueueClient{}
			ctx := context.Background()

			logHandler := tc.createLogHandler(next, client)
			if err := logHandler.Handle(ctx, tc.record); err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			if tc.shouldForward && len(next.handledRecords) == 0 {
				t.Fatalf("expected forwarding to next handler")
			}
			if !tc.shouldForward && len(next.handledRecords) != 0 {
				t.Fatalf("did not expect forwarding, got %d", len(next.handledRecords))
			}

			if tc.shouldEnqueue && logHandler.client == nil {
				t.Fatalf("we can't check for enqueued messages without a client")
			}
			if logHandler.client != nil {
				if tc.shouldEnqueue && len(client.enqueuedMsgs) != 1 {
					t.Fatalf("expected 1 enqueue, got %d", len(client.enqueuedMsgs))
				}
				if !tc.shouldEnqueue && len(client.enqueuedMsgs) != 0 {
					t.Fatalf("expected 0 enqueues, got %d", len(client.enqueuedMsgs))
				}
			}
		})
	}
}

func TestErrorExtractor_ExtractDescription(t *testing.T) {
	testError := errors.New("this-is-an-error")
	wrappedError := wrappedError{err: testError}
	extractor := ErrorExtractor{ErrorKeys: []string{"error", "err"}, Fallback: "fallback-loaded"}

	tests := map[string]struct {
		rec  slog.Record
		want string
	}{
		"extraction of error by key": {
			rec:  createLogRecord(slog.LevelError, "test", slog.Any("error", testError)),
			want: "this-is-an-error",
		},
		"extraction of error by case-insensitive key": {
			rec:  createLogRecord(slog.LevelError, "test", slog.Any("ErR", testError)),
			want: "this-is-an-error",
		},
		"extraction of wrapped error by key": {
			rec:  createLogRecord(slog.LevelError, "test", slog.Any("err", wrappedError)),
			want: "wrap: this-is-an-error",
		},
		"extraction of error as group returns the first error found": {
			rec: createLogRecord(slog.LevelError, "test", slog.Group("err",
				slog.Any("foo", "bar"),
				slog.Any("err", testError),
				slog.Any("error", wrappedError),
			)),
			want: "this-is-an-error",
		},
		"in case of multiple error keys, the first one wins": {
			rec:  createLogRecord(slog.LevelError, "test", slog.Any("error", testError), slog.Any("err", wrappedError)),
			want: "this-is-an-error",
		},
		"no match returns fallback": {
			rec:  createLogRecord(slog.LevelError, "test", slog.String("note", "this is a test case")),
			want: "fallback-loaded",
		},
		"error key without error should return fallback": {
			rec:  createLogRecord(slog.LevelError, "test", slog.String("error", "i'm not an error")),
			want: "fallback-loaded",
		},
		"extraction via LogValuer": {
			rec:  createLogRecord(slog.LevelError, "test", slog.Any("error", errValuer{err: wrappedError})),
			want: "wrap: this-is-an-error",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := extractor.ExtractDescription(tc.rec)

			if got != tc.want {
				t.Fatalf("ExtractDescription failed got=%q, want=%q", got, tc.want)
			}
		})
	}
}

func TestSlogCaptureHandler_WithPropertiesFn(t *testing.T) {
	next := &fakeNextSlogHandler{isEnabled: true}
	client := &fakeEnqueueClient{}
	ctx := context.Background()

	handler := NewSlogCaptureHandler(next, client,
		WithMinCaptureLevel(slog.LevelWarn),
		WithDistinctIDFn(func(_ context.Context, _ slog.Record) string {
			return "test-user"
		}),
		WithPropertiesFn(func(_ context.Context, r slog.Record) Properties {
			props := NewProperties()
			r.Attrs(func(a slog.Attr) bool {
				props.Set(a.Key, a.Value.Any())
				return true
			})
			return props
		}),
	)

	record := createLogRecord(slog.LevelError, "test error",
		slog.String("environment", "production"),
		slog.Int("retry_count", 3),
	)

	if err := handler.Handle(ctx, record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(client.enqueuedMsgs) != 1 {
		t.Fatalf("expected 1 enqueued message, got %d", len(client.enqueuedMsgs))
	}

	exception, ok := client.enqueuedMsgs[0].(Exception)
	if !ok {
		t.Fatalf("expected Exception, got %T", client.enqueuedMsgs[0])
	}

	if exception.Properties == nil {
		t.Fatal("expected Properties to be set")
	}

	// Verify extracted properties (note: slog.Int converts to int64)
	expectedProps := map[string]interface{}{
		"environment": "production",
		"retry_count": int64(3),
	}

	for key, expected := range expectedProps {
		if exception.Properties[key] != expected {
			t.Errorf("property %s: expected %v (type %T), got %v (type %T)",
				key, expected, expected, exception.Properties[key], exception.Properties[key])
		}
	}
}
