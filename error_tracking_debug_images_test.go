package posthog

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubMainImage forces a known image (or none) so grouped-frame emission is
// deterministic regardless of the build environment. Tests using it must not
// run in parallel.
func stubMainImage(t *testing.T, info mainImageInfo) {
	t.Helper()
	prev := mainImageFn
	mainImageFn = func() mainImageInfo { return info }
	t.Cleanup(func() { mainImageFn = prev })
}

func coveringImage() mainImageInfo {
	return mainImageInfo{
		image: DebugImage{
			Type:      "elf",
			DebugID:   "eb985355-1cd0-2890-5a3d-85138a19cbf9",
			ImageAddr: "0x400000",
			ImageSize: 1 << 40,
			Arch:      "x86_64",
		},
		base: 0,
		end:  ^uint64(0),
		ok:   true,
	}
}

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

func TestNativeImageArch(t *testing.T) {
	arch := nativeImageArch()
	if arch == "amd64" || arch == "arm64" {
		t.Errorf("nativeImageArch() = %q, want shared vocabulary", arch)
	}
}

//go:noinline
func defaultCaptureSite() Exception {
	return NewDefaultException(time.Now(), "user-1", "GroupTest", "grouped capture test")
}

// The real running executable, no stub: an integration check that discovered
// images are well formed and referenced by frames. Skips where the test
// binary carries no identity (Linux Go test binaries have no GNU build id).
func TestRealDebugImages(t *testing.T) {
	exception := defaultCaptureSite()

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
	stubMainImage(t, coveringImage())
	exception := defaultCaptureSite()
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

func TestSlogHandlerAttachesDebugImages(t *testing.T) {
	stubMainImage(t, coveringImage())

	client := &fakeEnqueueClient{}
	next := &fakeNextSlogHandler{isEnabled: false}
	handler := NewSlogCaptureHandler(next, client,
		WithDistinctIDFn(func(_ context.Context, _ slog.Record) string { return "user-1" }),
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
	if len(exception.DebugImages) != 1 {
		t.Fatalf("expected the default extractor to provide debug images")
	}
	frames := exception.ExceptionList[0].Stacktrace.Frames
	if len(frames) == 0 || frames[len(frames)-1].Platform != "native" {
		t.Errorf("expected native frames from the covered image")
	}
}
