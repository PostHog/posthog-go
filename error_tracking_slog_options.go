package posthog

import (
	"context"
	"log/slog"
)

type captureConfig struct {
	// minCaptureLevel is the lowest slog.Level at which a log record
	// will be sent to PostHog. Records below this level are ignored.
	// Default: slog.LevelWarn.
	minCaptureLevel slog.Level

	// distinctID returns the user/device identifier to associate with
	// this exception. If it returns an empty string, the exception will
	// not be sent. Typical implementations extract the user ID from
	// the provided context or log record.
	distinctID func(ctx context.Context, r slog.Record) string

	// fingerprint optionally computes a custom exception fingerprint
	// for grouping similar errors together in PostHog. If nil, PostHog will
	// assign a fingerprint during processing.
	fingerprint func(ctx context.Context, r slog.Record) *string

	// skip is the number of initial stack frames to omit when collecting
	// the stack trace for this capture. This helps remove internal/logging
	// frames from the output. Default: 5.
	skip int

	// stackTraceExtractor determines how stack traces are collected
	// and transformed into PostHog-compatible data structures.
	stackTraceExtractor StackTraceExtractor

	// descriptionExtractor extracts a human-readable description to
	// enrich the captured exception. By default, it searches the log
	// record for fields like "err" or "error" and uses the extracted
	// error message as the description.
	descriptionExtractor DescriptionExtractor

	// properties optionally returns custom properties to attach to the
	// captured exception event. Returning nil attaches no extra properties.
	// Built-in exception keys (e.g. "$lib", "distinct_id") win on collision.
	properties func(ctx context.Context, r slog.Record) Properties
}

func defaultCaptureConfig() captureConfig {
	return captureConfig{
		minCaptureLevel: slog.LevelWarn,
		distinctID: func(ctx context.Context, r slog.Record) string {
			return ""
		},
		fingerprint: func(ctx context.Context, r slog.Record) *string {
			return nil
		},
		skip: 5,
		stackTraceExtractor: DefaultStackTraceExtractor{
			InAppDecider: SimpleInAppDecider,
		},
		descriptionExtractor: ErrorExtractor{
			ErrorKeys: []string{"err", "error"},
			Fallback:  "<no linked error>",
		},
		properties: func(ctx context.Context, r slog.Record) Properties {
			return nil
		},
	}
}

// SlogOption customizes NewSlogCaptureHandler.
type SlogOption func(*captureConfig)

// WithMinCaptureLevel sets the minimum slog level captured as a PostHog exception.
// Records below this level are still passed to the wrapped handler.
func WithMinCaptureLevel(l slog.Level) SlogOption {
	return func(c *captureConfig) { c.minCaptureLevel = l }
}

// WithDistinctIDFn sets the function used to resolve the exception DistinctId.
// The fn parameter must be non-nil. If fn returns an empty string, the slog record is not captured.
func WithDistinctIDFn(fn func(ctx context.Context, r slog.Record) string) SlogOption {
	return func(c *captureConfig) { c.distinctID = fn }
}

// WithFingerprintFn sets the function used to compute an optional exception fingerprint.
// The fn parameter must be non-nil.
func WithFingerprintFn(fn func(ctx context.Context, r slog.Record) *string) SlogOption {
	return func(c *captureConfig) { c.fingerprint = fn }
}

// WithSkip sets how many stack frames the stack trace extractor should skip.
// Values less than or equal to zero are ignored.
func WithSkip(n int) SlogOption {
	return func(c *captureConfig) {
		if n > 0 {
			c.skip = n
		}
	}
}

// WithStackTraceExtractor sets the StackTraceExtractor used for captured exceptions.
// The extractor parameter must be non-nil.
func WithStackTraceExtractor(extractor StackTraceExtractor) SlogOption {
	return func(c *captureConfig) { c.stackTraceExtractor = extractor }
}

// WithDescriptionExtractor sets the DescriptionExtractor used for captured exceptions.
// The extractor parameter must be non-nil.
func WithDescriptionExtractor(extractor DescriptionExtractor) SlogOption {
	return func(c *captureConfig) { c.descriptionExtractor = extractor }
}

// WithPropertiesFn sets a function that returns custom properties for captured exceptions.
// Built-in exception properties take precedence if keys collide.
func WithPropertiesFn(fn func(ctx context.Context, r slog.Record) Properties) SlogOption {
	return func(c *captureConfig) {
		if fn != nil {
			c.properties = fn
		}
	}
}
