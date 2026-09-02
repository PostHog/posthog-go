package posthogotel

import (
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// aiSpanPrefixes are the known AI semantic convention prefixes.
var aiSpanPrefixes = []string{"gen_ai.", "llm.", "ai.", "traceloop."}

// IsAISpan reports whether a span follows a known AI semantic convention.
// It returns true when the span name, or any of its attribute keys, starts
// with one of the aiSpanPrefixes.
func IsAISpan(span sdktrace.ReadOnlySpan) bool {
	if hasAIPrefix(span.Name()) {
		return true
	}
	for _, attr := range span.Attributes() {
		if hasAIPrefix(string(attr.Key)) {
			return true
		}
	}
	return false
}

func hasAIPrefix(s string) bool {
	for _, prefix := range aiSpanPrefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// filterAISpans returns the subset of spans that IsAISpan accepts.
func filterAISpans(spans []sdktrace.ReadOnlySpan) []sdktrace.ReadOnlySpan {
	aiSpans := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		if IsAISpan(span) {
			aiSpans = append(aiSpans, span)
		}
	}
	return aiSpans
}
