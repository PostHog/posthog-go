package posthog

import (
	"context"
	"log/slog"
	"strings"
)

// SlogCaptureHandler wraps a slog.Handler and mirrors qualifying records
// to PostHog error tracking using client.Enqueue(Exception{...}).
type SlogCaptureHandler struct {
	next   slog.Handler
	client EnqueueClient
	cfg    captureConfig
}

// NewSlogCaptureHandler creates a new log handler wrapper.
// You typically wrap your existing handler:
//
//	client, err := posthog.NewWithConfig(...)
//	// error handling and `defer client.Close()` call
//	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
//	logger := slog.New(posthog.NewSlogCaptureHandler(base, client,
//	  posthog.WithDistinctIDFn(func(ctx context.Context, r slog.Record) string {
//	    return "my-user-id" // or pull from ctx
//	  }),
//	))
func NewSlogCaptureHandler(next slog.Handler, client EnqueueClient, opts ...SlogOption) *SlogCaptureHandler {
	cfg := defaultCaptureConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &SlogCaptureHandler{next: next, client: client, cfg: cfg}
}

func (h *SlogCaptureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	// Ensure we get called for capture-level records even if the base doesn't log them.
	if level >= h.cfg.minCaptureLevel {
		return true
	}

	return h.next.Enabled(ctx, level)
}

func (h *SlogCaptureHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always forward to the wrapped handler first.
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}

	// Then, non-intrusively mirror to PostHog if configured.
	if h.client == nil || r.Level < h.cfg.minCaptureLevel {
		return err
	}
	distinctID := h.cfg.distinctID(ctx, r)
	if distinctID == "" {
		return err
	}

	ex := Exception{
		DistinctId: distinctID,
		Timestamp:  r.Time,
		ExceptionList: []ExceptionItem{
			{
				Type:       r.Message,
				Value:      h.cfg.descriptionExtractor.ExtractDescription(r),
				Stacktrace: h.cfg.stackTraceExtractor.GetStackTrace(h.cfg.skip),
			},
		},
		ExceptionFingerprint: h.cfg.fingerprint(ctx, r),
	}
	_ = h.client.Enqueue(ex) // ignore enqueue error to keep logging safe

	return err
}

func (h *SlogCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SlogCaptureHandler{
		next:   h.next.WithAttrs(attrs),
		client: h.client,
		cfg:    h.cfg,
	}
}

func (h *SlogCaptureHandler) WithGroup(name string) slog.Handler {
	return &SlogCaptureHandler{
		next:   h.next.WithGroup(name),
		client: h.client,
		cfg:    h.cfg,
	}
}

// DescriptionExtractor defines the interface for extracting a human-readable
// description from a slog.Record for use in exception capture.
//
// Implementations should always return a non-empty string; returning an empty
// string will prevent the error from being captured.
type DescriptionExtractor interface {
	ExtractDescription(r slog.Record) string
}

// ErrorExtractor implements DescriptionExtractor by scanning a slog.Record
// for attributes containing an error.
//
// ErrorKeys specifies the list of attribute keys (case-insensitive) to check.
// The first matching attribute with an error value is returned as the
// description. If no matching error is found, the Fallback is returned.
type ErrorExtractor struct {
	ErrorKeys []string
	Fallback  string
}

func (e ErrorExtractor) ExtractDescription(r slog.Record) string {
	var found error

	normalisedKeys := make([]string, len(e.ErrorKeys))
	for i, k := range e.ErrorKeys {
		normalisedKeys[i] = strings.ToLower(k)
	}

	r.Attrs(func(a slog.Attr) bool {
		for _, errorKey := range normalisedKeys {
			if strings.ToLower(a.Key) == errorKey {
				if v := e.errorFromValue(a.Value); v != nil {
					found = v
					return false
				}
			}
		}
		return true
	})
	if found != nil {
		return found.Error()
	}

	return e.Fallback
}

func (e ErrorExtractor) errorFromValue(v slog.Value) error {
	switch v.Kind() {
	case slog.KindAny:
		any := v.Any()
		if any == nil {
			return nil
		}
		// direct error
		if e, ok := any.(error); ok && e != nil {
			return e
		}
		// common wrappers
		type unwrappable interface{ Unwrap() error }
		if c, ok := any.(unwrappable); ok {
			if e := c.Unwrap(); e != nil {
				return e
			}
		}

		return nil
	case slog.KindGroup:
		for _, ga := range v.Group() {
			if e := e.errorFromValue(ga.Value); e != nil {
				return e
			}
		}

		return nil
	case slog.KindLogValuer:
		return e.errorFromValue(v.Resolve())
	default:
		return nil
	}
}
