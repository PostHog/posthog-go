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
		"the basic happy path with no skip or function depth": {
			functionCallDepth: 0,
			skip:              0,
			expectedNames: []string{
				"runtime.Callers",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
				"testing.tRunner",
				"runtime.goexit",
			},
		},
		"skipping will remove entries from the head of the list, useful to omit internal function calls that add no value": {
			functionCallDepth: 0,
			skip:              3,
			expectedNames: []string{
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
				"testing.tRunner",
				"runtime.goexit",
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
				"runtime.Callers",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
				// +1 total for the 'actual' function call
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.recursiveChain",
				"github.com/posthog/posthog-go.TestDefaultStackTraceExtractor_GetStackTrace.func1",
				"testing.tRunner",
				"runtime.goexit",
			},
		},
		"once we exceed the pre-configured limit of 64, we will start to drop traces at the tail end": {
			functionCallDepth: 77,
			skip:              0,
			expectedNames: []string{
				"runtime.Callers",
				"github.com/posthog/posthog-go.DefaultStackTraceExtractor.GetStackTrace",
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
