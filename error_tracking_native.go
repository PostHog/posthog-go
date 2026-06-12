package posthog

import (
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// DebugImage describes a binary image loaded into the process, sent as the
// event-level $debug_images property so PostHog can symbolicate raw frame
// addresses against debug symbols uploaded with
// `posthog-cli debug-symbols upload`.
type DebugImage struct {
	// Type is the image format: "elf" on Linux, "macho" on macOS.
	Type string `json:"type"`
	// DebugID identifies the uploaded symbol set for this image.
	DebugID string `json:"debug_id"`
	// CodeID is the full code identifier (the complete GNU build ID on ELF).
	CodeID string `json:"code_id,omitempty"`
	// ImageAddr is the runtime load address of the image, as hex.
	ImageAddr string `json:"image_addr"`
	// ImageSize is the loaded extent of the image in bytes.
	ImageSize uint64 `json:"image_size,omitempty"`
	// ImageVmaddr is the link-time base address of the image, as hex.
	ImageVmaddr string `json:"image_vmaddr,omitempty"`
	// CodeFile is the path of the binary on disk.
	CodeFile string `json:"code_file,omitempty"`
	// Arch is the processor architecture the image was built for.
	Arch string `json:"arch"`
}

// mainImageInfo is the cached result of inspecting the running executable.
type mainImageInfo struct {
	image DebugImage
	base  uint64
	end   uint64
	ok    bool
}

var (
	mainImageOnce   sync.Once
	mainImageCached mainImageInfo
)

// mainImage inspects the running executable once and caches the result; the
// binary cannot change while the process runs.
func mainImage() mainImageInfo {
	mainImageOnce.Do(func() {
		mainImageCached = loadMainImage()
	})
	return mainImageCached
}

// NativeStackTraceExtractor captures stack traces with raw instruction
// addresses and reports the executable as a debug image, enabling
// server-side symbolication (source context, exact inline expansion) against
// uploaded debug symbols. Frames keep the runtime-resolved function, file,
// and line as a fallback for when no symbols are uploaded.
//
// Compared to DefaultStackTraceExtractor this emits `platform: "native"`
// frames, which require a PostHog version that understands them; older
// self-hosted instances should keep using the default extractor.
type NativeStackTraceExtractor struct {
	// InAppDecider decides whether each runtime frame is application code.
	// Set it to SimpleInAppDecider or another non-nil function before use.
	InAppDecider InAppDecider
}

// GetStackTrace captures the current goroutine stack in wire order (outermost
// frame first, capture site last). The skip parameter omits leading
// runtime.Callers frames before conversion.
func (n NativeStackTraceExtractor) GetStackTrace(skip int) *ExceptionStacktrace {
	pcs := make([]uintptr, 64)
	stackCallCount := runtime.Callers(skip, pcs)

	image := mainImage()

	traces := make([]StackFrame, 0, stackCallCount)
	// One wire frame per physical stack frame: the server re-derives the
	// full inline chain from each frame's address, so sending the entries
	// runtime.Callers synthesizes for inlined calls would duplicate their
	// parents after symbolication. The innermost entry of each group is the
	// one whose address expands to the whole chain, and it carries the most
	// precise attribution for display.
	i := 0
	for i < stackCallCount {
		pc := pcs[i]
		i++
		frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
		if frame == (runtime.Frame{}) {
			continue
		}

		stackFrame := StackFrame{
			Filename:  frame.File,
			LineNo:    frame.Line,
			Function:  frame.Function,
			InApp:     n.InAppDecider(frame),
			Synthetic: false,
			Platform:  "native",
			Lang:      "go",
			// The raw return address: the server subtracts one to target the
			// call instruction, the same contract as the other native SDKs.
			// (frame.PC is already adjusted, so sending it would shift the
			// lookup one instruction too far.)
			InstructionAddr: fmt.Sprintf("0x%x", uint64(pc)),
		}
		if frame.Entry != 0 {
			stackFrame.SymbolAddr = fmt.Sprintf("0x%x", uint64(frame.Entry))
		}
		if image.ok && uint64(pc) >= image.base && uint64(pc) < image.end {
			stackFrame.ImageAddr = image.image.ImageAddr
		}

		traces = append(traces, stackFrame)

		// An inlined entry (Func == nil) is followed by synthesized entries
		// for its parent(s) within the same physical frame, ending at the
		// physical function (Func != nil): swallow them.
		if frame.Func == nil {
			for i < stackCallCount {
				parent, _ := runtime.CallersFrames([]uintptr{pcs[i]}).Next()
				i++
				if parent == (runtime.Frame{}) || parent.Func != nil {
					break
				}
			}
		}
	}

	// Reverse to wire order: outermost first, capture site last.
	for i, j := 0, len(traces)-1; i < j; i, j = i+1, j-1 {
		traces[i], traces[j] = traces[j], traces[i]
	}

	return &ExceptionStacktrace{
		Type:   "raw",
		Frames: traces,
	}
}

// DebugImageProvider is implemented by stack trace extractors that can also
// report the binary images their frames reference. Integrations that accept a
// StackTraceExtractor (e.g. the slog capture handler) use it to attach
// $debug_images to the exceptions they build.
type DebugImageProvider interface {
	GetDebugImages() []DebugImage
}

// GetDebugImages returns the debug images referenced by captured frames —
// currently the main executable, when its identity could be determined.
func (n NativeStackTraceExtractor) GetDebugImages() []DebugImage {
	image := mainImage()
	if !image.ok {
		return nil
	}
	return []DebugImage{image.image}
}

// NewNativeException builds an Exception like NewDefaultException, but with
// raw frame addresses and debug images for server-side symbolication. Upload
// matching debug symbols with `posthog-cli debug-symbols upload`.
func NewNativeException(
	timestamp time.Time,
	distinctID, title, description string,
) Exception {
	extractor := NativeStackTraceExtractor{InAppDecider: SimpleInAppDecider}

	return Exception{
		DistinctId: distinctID,
		Timestamp:  timestamp,
		ExceptionList: []ExceptionItem{
			{
				Type:       title,
				Value:      description,
				Stacktrace: extractor.GetStackTrace(3),
			},
		},
		DebugImages: extractor.GetDebugImages(),
	}
}

// debugIDFromGNUBuildID derives a debug id from a GNU build id the same way
// the PostHog server and CLI derive it from the binary: the first 16 bytes
// interpreted as a little-endian GUID (first three fields byte-swapped),
// zero-padded when the build id is shorter.
func debugIDFromGNUBuildID(buildID []byte) string {
	if len(buildID) == 0 {
		return ""
	}
	var data [16]byte
	copy(data[:], buildID)
	reverse := func(b []byte) {
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}
	}
	reverse(data[0:4])
	reverse(data[4:6])
	reverse(data[6:8])
	return formatUUID(data)
}

func formatUUID(data [16]byte) string {
	hexStr := hex.EncodeToString(data[:])
	return hexStr[0:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:32]
}

// nativeImageArch maps GOARCH to the architecture vocabulary shared by the
// other PostHog SDKs and debug formats.
func nativeImageArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}
