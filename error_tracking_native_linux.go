//go:build linux

package posthog

import (
	"bufio"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// loadMainImage inspects the running executable: its mapped address range
// from /proc/self/maps and its GNU build id from the ELF notes. Returns a
// zero-value info (ok=false) when either is unavailable — e.g. the binary
// was linked without `-ldflags=-B=gobuildid`.
func loadMainImage() mainImageInfo {
	exe, err := os.Executable()
	if err != nil {
		return mainImageInfo{}
	}

	base, end, ok := executableMappedRange(exe)
	if !ok {
		return mainImageInfo{}
	}

	// Open the magic link rather than the resolved path: it always reads the
	// mapped image, even after the binary on disk is replaced or deleted
	// (e.g. mid-deploy).
	file, err := elf.Open("/proc/self/exe")
	if err != nil {
		return mainImageInfo{}
	}
	defer file.Close()

	buildID := gnuBuildID(file)
	if len(buildID) == 0 {
		return mainImageInfo{}
	}

	vmaddr := uint64(0)
	for _, prog := range file.Progs {
		if prog.Type == elf.PT_LOAD {
			vmaddr = prog.Vaddr
			break
		}
	}

	return mainImageInfo{
		image: DebugImage{
			Type:        "elf",
			DebugID:     debugIDFromGNUBuildID(buildID),
			CodeID:      hex.EncodeToString(buildID),
			ImageAddr:   fmt.Sprintf("0x%x", base),
			ImageSize:   end - base,
			ImageVmaddr: fmt.Sprintf("0x%x", vmaddr),
			CodeFile:    exe,
			Arch:        nativeImageArch(),
		},
		base: base,
		end:  end,
		ok:   true,
	}
}

// executableMappedRange parses /proc/self/maps and returns the address range
// covered by the executable's own mappings.
func executableMappedRange(exe string) (base, end uint64, ok bool) {
	file, err := os.Open("/proc/self/maps")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// Format: start-end perms offset dev inode path
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 || fields[5] != exe {
			continue
		}
		addrs := strings.SplitN(fields[0], "-", 2)
		if len(addrs) != 2 {
			continue
		}
		start, err1 := strconv.ParseUint(addrs[0], 16, 64)
		stop, err2 := strconv.ParseUint(addrs[1], 16, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if !ok || start < base {
			base = start
		}
		if stop > end {
			end = stop
		}
		ok = true
	}

	return base, end, ok
}

// gnuBuildID extracts the NT_GNU_BUILD_ID note from an ELF file. Returns nil
// when the binary carries no GNU build id.
func gnuBuildID(file *elf.File) []byte {
	for _, section := range file.Sections {
		if section.Type != elf.SHT_NOTE {
			continue
		}
		data, err := section.Data()
		if err != nil {
			continue
		}
		if id := parseGNUBuildIDNote(data, file.ByteOrder); id != nil {
			return id
		}
	}
	return nil
}

// parseGNUBuildIDNote walks ELF note records; their header fields use the
// file's own byte order.
func parseGNUBuildIDNote(data []byte, byteOrder binary.ByteOrder) []byte {
	const gnuBuildIDType = 3
	for len(data) >= 12 {
		nameSize := byteOrder.Uint32(data[0:4])
		descSize := byteOrder.Uint32(data[4:8])
		noteType := byteOrder.Uint32(data[8:12])
		data = data[12:]

		alignedName := (nameSize + 3) &^ 3
		alignedDesc := (descSize + 3) &^ 3
		if uint64(len(data)) < uint64(alignedName)+uint64(alignedDesc) {
			return nil
		}
		name := data[:nameSize]
		desc := data[alignedName : alignedName+descSize]
		data = data[alignedName+alignedDesc:]

		if noteType == gnuBuildIDType && bytes.Equal(name, []byte("GNU\x00")) {
			return desc
		}
	}
	return nil
}
