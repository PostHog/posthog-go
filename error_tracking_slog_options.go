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

	// properties computes additional event properties to attach
	// to the captured exception event.
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
			return NewProperties()
		},
	}
}

type SlogOption func(*captureConfig)

func WithMinCaptureLevel(l slog.Level) SlogOption {
	return func(c *captureConfig) { c.minCaptureLevel = l }
}

func WithDistinctIDFn(fn func(ctx context.Context, r slog.Record) string) SlogOption {
	return func(c *captureConfig) { c.distinctID = fn }
}

func WithFingerprintFn(fn func(ctx context.Context, r slog.Record) *string) SlogOption {
	return func(c *captureConfig) { c.fingerprint = fn }
}

func WithSkip(n int) SlogOption {
	return func(c *captureConfig) {
		if n > 0 {
			c.skip = n
		}
	}
}

func WithStackTraceExtractor(extractor StackTraceExtractor) SlogOption {
	return func(c *captureConfig) { c.stackTraceExtractor = extractor }
}

func WithDescriptionExtractor(extractor DescriptionExtractor) SlogOption {
	return func(c *captureConfig) { c.descriptionExtractor = extractor }
}

func WithPropertiesFn(fn func(ctx context.Context, r slog.Record) Properties) SlogOption {
	return func(c *captureConfig) { c.properties = fn }
}
