//go:build darwin

package posthog

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenMachOUniversal wraps the running test binary in a universal (fat)
// container and checks that openMachO picks the slice for the running
// architecture, since macho.Open alone rejects fat files.
func TestOpenMachOUniversal(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	thin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(thin) < 12 {
		t.Fatal("test binary too small")
	}

	// fat_header + one fat_arch entry (both big-endian), then the thin slice
	// at an aligned offset; cputype/cpusubtype copied from the thin header.
	const sliceOffset = 0x4000
	fat := make([]byte, sliceOffset+len(thin))
	binary.BigEndian.PutUint32(fat[0:4], 0xcafebabe) // FAT_MAGIC
	binary.BigEndian.PutUint32(fat[4:8], 1)          // nfat_arch
	binary.BigEndian.PutUint32(fat[8:12], binary.LittleEndian.Uint32(thin[4:8]))
	binary.BigEndian.PutUint32(fat[12:16], binary.LittleEndian.Uint32(thin[8:12]))
	binary.BigEndian.PutUint32(fat[16:20], sliceOffset)
	binary.BigEndian.PutUint32(fat[20:24], uint32(len(thin)))
	binary.BigEndian.PutUint32(fat[24:28], 14) // 2^14 page alignment
	copy(fat[sliceOffset:], thin)

	path := filepath.Join(t.TempDir(), "universal")
	if err := os.WriteFile(path, fat, 0o755); err != nil {
		t.Fatal(err)
	}
	universal, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer universal.Close()

	file, err := openMachO(universal)
	if err != nil {
		t.Fatalf("openMachO(universal) = %v", err)
	}

	if _, ok := machoUUID(file); !ok {
		t.Error("expected LC_UUID in the selected slice")
	}
	if file.Segment("__TEXT") == nil {
		t.Error("expected __TEXT segment in the selected slice")
	}
}
