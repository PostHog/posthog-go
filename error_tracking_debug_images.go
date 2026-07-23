package posthog

import (
	"encoding/hex"
	"runtime"
	"sync"
)

// DebugImage describes a binary image loaded into the process, sent as the
// event-level $debug_images property so PostHog can symbolicate raw frame
// addresses against debug symbols uploaded with
// `posthog-cli symbol-sets upload`.
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

// DebugImageProvider is implemented by stack trace extractors that can also
// report the binary images their frames reference. Integrations that accept a
// StackTraceExtractor (e.g. the slog capture handler) use it to attach
// $debug_images to the exceptions they build.
type DebugImageProvider interface {
	GetDebugImages() []DebugImage
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

// mainImageFn is the seam tests use to force a known image (or none), keeping
// grouped-frame emission deterministic across build environments.
var mainImageFn = mainImage

// debugIDFromGNUBuildID derives a debug id from a GNU build id the same way
// the PostHog server and CLI derive it from the binary (symbolic's
// compute_debug_id): the first 16 bytes as a GUID, zero-padded when shorter,
// with the first three fields byte-swapped only for little-endian ELF files.
func debugIDFromGNUBuildID(buildID []byte, littleEndian bool) string {
	if len(buildID) == 0 {
		return ""
	}
	var data [16]byte
	copy(data[:], buildID)
	if littleEndian {
		reverse := func(b []byte) {
			for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
				b[i], b[j] = b[j], b[i]
			}
		}
		reverse(data[0:4])
		reverse(data[4:6])
		reverse(data[6:8])
	}
	return formatUUID(data)
}

func formatUUID(data [16]byte) string {
	hexStr := hex.EncodeToString(data[:])
	return hexStr[0:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:32]
}

// nativeImageArch maps GOARCH to the architecture vocabulary shared by the
// other PostHog SDKs ("arm64" is already the shared spelling — posthog-rs
// normalizes aarch64 to it).
func nativeImageArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}
