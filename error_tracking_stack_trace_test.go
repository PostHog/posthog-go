package posthog

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestSimpleInAppDecider(t *testing.T) {
	tests := map[string]struct {
		filePath string
		expected bool
	}{
		"toolchain under module cache => not in app": {
			filePath: filepath.Join("gopath", "pkg", "mod", "golang.org", "toolchain@v0.0.1-go1.24.0.darwin-amd64", "src", "net", "http", "server.go"),
			expected: false,
		},
		"vendored dependency => not in app": {
			filePath: filepath.Join("myrepo", "vendor", "github.com", "gin-gonic", "gin", "gin.go"),
			expected: false,
		},
		"general module cache => not in app": {
			filePath: filepath.Join("gopath", "pkg", "mod", "github.com", "someone", "pkg@v1.2.3", "x.go"),
			expected: false,
		},
		"normal repo file => in app": {
			filePath: filepath.Join("Users", "me", "dev", "repo", "pkg", "foo", "bar.go"),
			expected: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			actual := SimpleInAppDecider(runtime.Frame{File: tc.filePath})

			if actual != tc.expected {
				t.Fatalf("SimpleInAppDecider(%q) = %v, want %v",
					tc.filePath, actual, tc.expected)
			}
		})
	}
}

// go:inline - Avoid optimizations which might break the stack trace
func recursiveChain(ex DefaultStackTraceExtractor, skip, depth int) []StackFrame {
	if depth <= 0 {
		st := ex.GetStackTrace(skip)
		if st == nil {
			return nil
		}
		return st.Frames
	}
	return recursiveChain(ex, skip, depth-1)
}

func TestDefaultStackTraceExtractor_GetStackTrace(t *testing.T) {
	extractor := DefaultStackTraceExtractor{InAppDecider: SimpleInAppDecider}

	tests := map[string]struct {
		functionCallDepth int
		skip              int
		expectedNames     []string
	}{
		// Frames are emitted in canonical wire order: outermost (entry point,
		// runtime.goexit) first, capture site (runtime.Callers) last.
		"the basic happy path with no skip or function depth": {
			functionCallDepth: 0,
			skip:              0,
			expectedNames: []string{
				"runtime.goexit",
				"testing.tRunner",
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
				"runtime.Callers",
			},
		},
		"skipping removes the innermost frames, now at the tail, useful to omit internal function calls that add no value": {
			functionCallDepth: 0,
			skip:              3,
			expectedNames: []string{
				"runtime.goexit",
				"testing.tRunner",
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
			},
		},
		"calls with a very high skip level should return an empty stack trace": {
			functionCallDepth: 0,
			skip:              300,
			expectedNames:     []string{},
		},
		"if we have a longer function call depth, they will all show up in the trace": {
			functionCallDepth: 5,
			skip:              0,
			expectedNames: []string{
				"runtime.goexit",
				"testing.tRunner",
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
				// +1 total for the 'actual' function call
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
				"runtime.Callers",
			},
		},
		// The 64-frame capture limit drops the outermost frames (the runtime
		// stops yielding after 64 innermost entries), so after reversal the
		// entry-point frames are gone and the slice starts mid-chain.
		"once we exceed the pre-configured limit of 64, we will start to drop traces at the outermost end": {
			functionCallDepth: 77,
			skip:              0,
			expectedNames: []string{
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
				"runtime.Callers",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			traces := recursiveChain(extractor, tc.skip, tc.functionCallDepth)

			if len(traces) != len(tc.expectedNames) {
				t.Fatalf("trace length does not match expectations: got=%v want=%v",
					len(traces), len(tc.expectedNames))
			}

			for i := range traces {
				if traces[i].Function != tc.expectedNames[i] {
					t.Fatalf("trace resolved name does not match expectations (idx=%d): got=%v want=%v",
						i, traces[i].Function, tc.expectedNames[i])
				}
			}
		})
	}
}

// TestDefaultStackTraceExtractor_CanonicalWireOrder is an explicit regression
// guard for the canonical wire order shared across PostHog SDKs: frames run
// bottom-up, so the first frame is the outermost entry point and the last frame
// is the innermost capture site. Reversing this order would silently break
// server-side grouping and display.
func TestDefaultStackTraceExtractor_CanonicalWireOrder(t *testing.T) {
	extractor := DefaultStackTraceExtractor{InAppDecider: SimpleInAppDecider}

	traces := extractor.GetStackTrace(0)
	if traces == nil || len(traces.Frames) < 2 {
		t.Fatalf("expected at least two frames, got %v", traces)
	}

	if got := traces.Frames[0].Function; got != "runtime.goexit" {
		t.Errorf("first frame should be the outermost entry point: got=%q want=%q", got, "runtime.goexit")
	}

	if got := traces.Frames[len(traces.Frames)-1].Function; got != "runtime.Callers" {
		t.Errorf("last frame should be the innermost capture site: got=%q want=%q", got, "runtime.Callers")
	}
}
