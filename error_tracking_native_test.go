package posthog

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDebugIDFromGNUBuildID(t *testing.T) {
	// Vector verified against symbolic's ElfObject::debug_id, which the
	// PostHog server and CLI use: the first 16 bytes as a little-endian GUID.
	buildID, err := hex.DecodeString("555398ebd01c90285a3d85138a19cbf9bbcec352")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := debugIDFromGNUBuildID(buildID), "eb985355-1cd0-2890-5a3d-85138a19cbf9"; got != want {
		t.Errorf("debugIDFromGNUBuildID() = %q, want %q", got, want)
	}

	// Short build ids are zero-padded to 16 bytes
	if got, want := debugIDFromGNUBuildID([]byte{0xab, 0xcd}), "0000cdab-0000-0000-0000-000000000000"; got != want {
		t.Errorf("debugIDFromGNUBuildID(short) = %q, want %q", got, want)
	}

	if got := debugIDFromGNUBuildID(nil); got != "" {
		t.Errorf("debugIDFromGNUBuildID(nil) = %q, want empty", got)
	}
}

//go:noinline
func nativeCaptureSite() Exception {
	return NewNativeException(time.Now(), "user-1", "NativeTest", "native capture test")
}

func TestNativeStackTraceExtractor(t *testing.T) {
	exception := nativeCaptureSite()

	if len(exception.ExceptionList) != 1 {
		t.Fatalf("expected one exception item, got %d", len(exception.ExceptionList))
	}
	stacktrace := exception.ExceptionList[0].Stacktrace
	if stacktrace == nil || len(stacktrace.Frames) == 0 {
		t.Fatal("expected captured frames")
	}
	if stacktrace.Type != "raw" {
		t.Errorf("stacktrace type = %q, want raw", stacktrace.Type)
	}

	hexAddr := regexp.MustCompile(`^0x[0-9a-f]+$`)
	for i, frame := range stacktrace.Frames {
		if frame.Platform != "native" {
			t.Errorf("frame[%d].Platform = %q, want native", i, frame.Platform)
		}
		if frame.Lang != "go" {
			t.Errorf("frame[%d].Lang = %q, want go", i, frame.Lang)
		}
		if !hexAddr.MatchString(frame.InstructionAddr) {
			t.Errorf("frame[%d].InstructionAddr = %q, want hex", i, frame.InstructionAddr)
		}
	}

	// Wire order is outermost first, so the capture site is the last frame.
	crash := stacktrace.Frames[len(stacktrace.Frames)-1]
	if !strings.Contains(crash.Function, "nativeCaptureSite") {
		t.Errorf("crash frame = %q, want the capture site last", crash.Function)
	}

	// No duplicated addresses: one wire frame per captured pc, even where
	// the runtime expands inlined frames.
	seen := map[string]bool{}
	for _, frame := range stacktrace.Frames {
		if seen[frame.InstructionAddr] {
			t.Errorf("duplicate instruction_addr %q", frame.InstructionAddr)
		}
		seen[frame.InstructionAddr] = true
	}
}

func TestNativeDebugImages(t *testing.T) {
	exception := nativeCaptureSite()

	// The test binary's identity is discoverable on the supported platforms;
	// on Linux this requires the GNU build id, which test binaries built by
	// the Go toolchain don't carry, so allow absence there.
	if len(exception.DebugImages) == 0 {
		t.Skip("no debug image for the test binary on this platform/build")
	}

	image := exception.DebugImages[0]
	if image.Type != "elf" && image.Type != "macho" {
		t.Errorf("image.Type = %q", image.Type)
	}
	if len(image.DebugID) < 36 {
		t.Errorf("image.DebugID = %q, want uuid-shaped", image.DebugID)
	}
	if image.Arch == "amd64" || image.Arch == "arm64" {
		t.Errorf("image.Arch = %q, want shared vocabulary (x86_64/aarch64)", image.Arch)
	}

	base, err := strconv.ParseUint(strings.TrimPrefix(image.ImageAddr, "0x"), 16, 64)
	if err != nil || base == 0 {
		t.Fatalf("image.ImageAddr = %q, want non-zero hex", image.ImageAddr)
	}

	// Frames in the main executable reference the image's address, and their
	// instruction addresses fall inside its extent.
	frames := exception.ExceptionList[0].Stacktrace.Frames
	referenced := false
	for _, frame := range frames {
		if frame.ImageAddr != image.ImageAddr {
			continue
		}
		referenced = true
		addr, err := strconv.ParseUint(strings.TrimPrefix(frame.InstructionAddr, "0x"), 16, 64)
		if err != nil {
			t.Fatalf("frame.InstructionAddr = %q", frame.InstructionAddr)
		}
		if addr < base || addr >= base+image.ImageSize {
			t.Errorf("frame addr %#x outside image [%#x, %#x)", addr, base, base+image.ImageSize)
		}
	}
	if !referenced {
		t.Error("no frame references the main image")
	}
}

func TestExceptionAPIfyIncludesDebugImages(t *testing.T) {
	exception := nativeCaptureSite()
	if len(exception.DebugImages) == 0 {
		t.Skip("no debug image for the test binary on this platform/build")
	}
	exception.Type = "exception"

	payload, err := json.Marshal(exception.APIfy())
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Properties struct {
			DebugImages []DebugImage `json:"$debug_images"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Properties.DebugImages) != 1 {
		t.Fatalf("expected $debug_images in payload, got %s", payload)
	}
}

func TestNativeImageArch(t *testing.T) {
	arch := nativeImageArch()
	if arch == "amd64" || arch == "arm64" {
		t.Errorf("nativeImageArch() = %q, want shared vocabulary", arch)
	}
}

//go:noinline
func collapseCapture() Exception {
	return NewNativeException(time.Now(), "user-1", "InlineTest", "inline collapse test")
}

// collapseLeaf is cheap enough for the compiler to inline into its caller;
// the runtime then synthesizes separate pcs entries for it and its parent
// within one physical frame, which the extractor must collapse.
func collapseLeaf(value int) (Exception, int) {
	doubled := value * 2
	doubled += value / 3
	exception := collapseCapture()
	return exception, doubled + 5
}

//go:noinline
func collapseParent(value int) Exception {
	exception, _ := collapseLeaf(value)
	return exception
}

func TestNativeExtractorCollapsesInlineFrames(t *testing.T) {
	exception := collapseParent(len(t.Name()))
	frames := exception.ExceptionList[0].Stacktrace.Frames

	var leaf, parent *StackFrame
	for i := range frames {
		if strings.Contains(frames[i].Function, "collapseLeaf") {
			leaf = &frames[i]
		}
		if strings.Contains(frames[i].Function, "collapseParent") {
			parent = &frames[i]
		}
	}

	if leaf == nil {
		t.Skip("collapseLeaf frame not captured")
	}
	if parent != nil && parent.SymbolAddr != leaf.SymbolAddr {
		t.Skip("collapseLeaf was not inlined by this toolchain")
	}
	// When inlined, leaf and parent share one physical frame; the server
	// re-derives the parent from the leaf's address, so it must not also be
	// sent as its own wire frame.
	if parent != nil {
		t.Error("expected the inlined frame's parent to be collapsed away")
	}
}

func TestSlogHandlerAttachesDebugImages(t *testing.T) {
	client := &fakeEnqueueClient{}
	next := &fakeNextSlogHandler{isEnabled: false}
	handler := NewSlogCaptureHandler(next, client,
		WithDistinctIDFn(func(_ context.Context, _ slog.Record) string { return "user-1" }),
		WithStackTraceExtractor(NativeStackTraceExtractor{InAppDecider: SimpleInAppDecider}),
	)

	if err := handler.Handle(context.Background(), createLogRecord(slog.LevelError, "boom")); err != nil {
		t.Fatal(err)
	}
	if len(client.enqueuedMsgs) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(client.enqueuedMsgs))
	}

	exception, ok := client.enqueuedMsgs[0].(Exception)
	if !ok {
		t.Fatalf("expected Exception, got %T", client.enqueuedMsgs[0])
	}
	if len(exception.DebugImages) == 0 {
		t.Skip("no debug image for the test binary on this platform/build")
	}
	frames := exception.ExceptionList[0].Stacktrace.Frames
	if len(frames) == 0 || frames[0].Platform != "native" {
		t.Errorf("expected native frames from the configured extractor")
	}
}
