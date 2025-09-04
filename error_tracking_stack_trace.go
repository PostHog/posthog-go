package posthog

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// InAppDecider reports whether a stack frame should be considered “in-app”
// (i.e., part of the calling application’s own code) as opposed to external
// dependencies, standard library, or tooling internals. The UI can use this
// signal to visually emphasize frames that are likely actionable.
type InAppDecider func(frame runtime.Frame) bool

// SimpleInAppDecider a naive implementation of InAppDecider which checks the file paths.
// It's good enough for 90% of the cases.
func SimpleInAppDecider(frame runtime.Frame) bool {
	f := frame.File
	sep := string(filepath.Separator)

	if strings.Contains(f, "pkg/mod/golang.org/toolchain") ||
		strings.Contains(f, fmt.Sprintf("%[1]svendor%[1]s", sep)) ||
		strings.Contains(f, fmt.Sprintf("%[1]spkg%[1]smod%[1]s", sep)) {
		return false
	}

	return true
}

// StackTraceExtractor produces a stack trace for the current goroutine to
// enrich captured exceptions with call-site context.
type StackTraceExtractor interface {
	// GetStackTrace returns a PostHog-compatible stack trace.
	//
	// The skip parameter controls how many leading frames to omit before
	// recording. Use it to drop extractor/logging internals and start at the
	// application call site. For example, a skip of 3–5 is typically enough to
	// hide wrapper layers when called from a slog handler.
	GetStackTrace(skip int) *ExceptionStacktrace
}

// DefaultStackTraceExtractor is provided by default as a sane / simple implementation
// of a StackTraceExtractor. It should be enough for most use cases, however, you're free to
// create your own implementation if you require more flexibility.
type DefaultStackTraceExtractor struct {
	InAppDecider InAppDecider
}

func (d DefaultStackTraceExtractor) GetStackTrace(skip int) *ExceptionStacktrace {
	pcs := make([]uintptr, 64)
	stackCallCount := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:stackCallCount])

	traces := make([]StackFrame, 0, stackCallCount)
	for {
		frame, hasMore := frames.Next()
		if frame == *new(runtime.Frame) {
			break
		}

		traces = append(traces, StackFrame{
			Filename:  frame.File,
			LineNo:    frame.Line,
			Function:  frame.Function,
			InApp:     d.InAppDecider(frame),
			Synthetic: false,
			Platform:  "go",
		})
		if !hasMore {
			break
		}
	}

	return &ExceptionStacktrace{
		Type:   "raw",
		Frames: traces,
	}
}
