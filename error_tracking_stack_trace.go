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

// SimpleInAppDecider is a basic InAppDecider that classifies frames by file path.
// It excludes vendored modules, module cache paths, and Go toolchain paths.
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
	// InAppDecider decides whether each runtime frame is application code.
	// Set it to SimpleInAppDecider or another non-nil function before use.
	InAppDecider InAppDecider
}

// GetStackTrace captures the current goroutine stack in wire order (outermost
// frame first, capture site last). The skip parameter omits leading
// runtime.Callers frames before conversion.
//
// When the running executable's identity is known (see DebugImage), frames
// are emitted as client-expanded inline groups for server-side symbolication:
// each physical stack frame and the entries the runtime synthesizes for calls
// inlined into it share one raw return address, with the synthesized layers
// tagged inline. PostHog resolves that address once against uploaded debug
// symbols and atomically replaces the whole group with its own expansion
// (exact inline chain, source context), or keeps these runtime-resolved
// frames verbatim when no symbols are uploaded. When the executable can't be
// identified, frames keep the plain pre-existing shape.
func (d DefaultStackTraceExtractor) GetStackTrace(skip int) *ExceptionStacktrace {
	pcs := make([]uintptr, 64)
	stackCallCount := runtime.Callers(skip, pcs)

	image := mainImageFn()

	traces := make([]StackFrame, 0, stackCallCount)
	i := 0
	for i < stackCallCount {
		// One physical stack frame per iteration: resolve the leaf entry,
		// then collect the entries runtime.Callers synthesizes for its inline
		// ancestors, anchored by the physical frame (Func != nil) sharing the
		// same entry pc. Matching on the entry pc keeps non-Go frames (also
		// Func == nil, but with zero or distinct entries) and recursive calls
		// of the physical function separate.
		leafPC := pcs[i]
		i++
		expanded := runtime.CallersFrames([]uintptr{leafPC})
		leaf, more := expanded.Next()
		if leaf == (runtime.Frame{}) {
			continue
		}

		group := []runtime.Frame{leaf}
		// A single pc can expand to several frames: a cgo traceback
		// symbolizer (runtime.SetCgoTraceback) reports inlined C frames this
		// way. Drain them all; they stay unanchored (Entry 0) and fall through
		// to the plain emission below.
		for more {
			var frame runtime.Frame
			frame, more = expanded.Next()
			if frame == (runtime.Frame{}) {
				break
			}
			group = append(group, frame)
		}
		anchored := leaf.Func != nil
		if len(group) == 1 && leaf.Func == nil && leaf.Entry != 0 {
			for i < stackCallCount {
				parent, _ := runtime.CallersFrames([]uintptr{pcs[i]}).Next()
				if parent == (runtime.Frame{}) || parent.Entry != leaf.Entry {
					break
				}
				i++
				group = append(group, parent)
				if parent.Func != nil {
					anchored = true
					break
				}
			}
		}

		covered := image.ok && uint64(leafPC) >= image.base && uint64(leafPC) < image.end
		if !covered || !anchored {
			// No resolvable image for this address (or the pcs buffer cut the
			// group off from its physical frame): plain Go frames, the
			// pre-native wire shape.
			for _, frame := range group {
				traces = append(traces, StackFrame{
					Filename:  frame.File,
					LineNo:    frame.Line,
					Function:  frame.Function,
					InApp:     d.InAppDecider(frame),
					Synthetic: false,
					Platform:  "go",
				})
			}
			continue
		}

		// Client-expanded inline group. Every layer carries the group's raw
		// leaf address: the entry whose address the server expands to the
		// whole chain, with the server subtracting one to target the call
		// instruction (frame.PC is already adjusted, so sending it would
		// shift that lookup one instruction too far). The physical anchor is
		// the group's lead; the synthesized layers are marked inline.
		for idx, frame := range group {
			isAnchor := idx == len(group)-1
			stackFrame := StackFrame{
				Filename:        frame.File,
				LineNo:          frame.Line,
				Function:        frame.Function,
				InApp:           d.InAppDecider(frame),
				Synthetic:       false,
				Platform:        "native",
				Lang:            "go",
				ClientResolved:  true,
				Inline:          !isAnchor,
				InstructionAddr: fmt.Sprintf("0x%x", uint64(leafPC)),
				ImageAddr:       image.image.ImageAddr,
			}
			if frame.Entry != 0 {
				stackFrame.SymbolAddr = fmt.Sprintf("0x%x", uint64(frame.Entry))
			}
			traces = append(traces, stackFrame)
		}
	}

	// The runtime yields innermost first; reverse to the canonical wire order
	// shared across PostHog SDKs: outermost (entry point) first, capture site
	// last. The reversal also puts each group's physical frame ahead of its
	// inline members, the order the server-side group contract expects.
	for i, j := 0, len(traces)-1; i < j; i, j = i+1, j-1 {
		traces[i], traces[j] = traces[j], traces[i]
	}

	return &ExceptionStacktrace{
		Type:   "raw",
		Frames: traces,
	}
}

// GetDebugImages returns the debug images referenced by captured frames —
// currently the main executable, when its identity could be determined.
func (d DefaultStackTraceExtractor) GetDebugImages() []DebugImage {
	image := mainImageFn()
	if !image.ok {
		return nil
	}
	return []DebugImage{image.image}
}
