//go:build darwin

package posthog

import (
	"debug/gosym"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// pinnedExecutable is a handle to the binary this process was launched from,
// opened at init before anything can replace the launch path. macOS has no
// /proc/self/exe equivalent: reading the path later races with deploys or
// self-updates swapping the file, which would pair this process's addresses
// with a different build's UUID. The pinned inode always reads the mapped
// binary; the handle is kept for the process lifetime.
var pinnedExecutable struct {
	file *os.File
	path string
}

func init() {
	path, err := os.Executable()
	if err != nil {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	pinnedExecutable.file = file
	pinnedExecutable.path = path
}

// loadMainImage inspects the running executable: its Mach-O UUID (which
// `posthog-cli dsym upload` uses, uppercase, as the symbol set id) and its
// runtime load address, computed from the ASLR slide of a known function.
func loadMainImage() mainImageInfo {
	if pinnedExecutable.file == nil {
		return mainImageInfo{}
	}

	file, err := openMachO(pinnedExecutable.file)
	if err != nil {
		return mainImageInfo{}
	}

	uuid, ok := machoUUID(file)
	if !ok {
		return mainImageInfo{}
	}

	textSeg := file.Segment("__TEXT")
	if textSeg == nil {
		return mainImageInfo{}
	}

	slide, ok := machoSlide(file)
	if !ok {
		return mainImageInfo{}
	}

	base := textSeg.Addr + slide
	end := base
	for _, load := range file.Loads {
		if seg, isSeg := load.(*macho.Segment); isSeg {
			if segEnd := seg.Addr + seg.Memsz + slide; segEnd > end {
				end = segEnd
			}
		}
	}

	var data [16]byte
	copy(data[:], uuid[:])

	return mainImageInfo{
		image: DebugImage{
			Type: "macho",
			// Uppercase to match the symbol set ids stored by
			// `posthog-cli dsym upload`.
			DebugID:     strings.ToUpper(formatUUID(data)),
			ImageAddr:   fmt.Sprintf("0x%x", base),
			ImageSize:   end - base,
			ImageVmaddr: fmt.Sprintf("0x%x", textSeg.Addr),
			CodeFile:    pinnedExecutable.path,
			Arch:        nativeImageArch(),
		},
		base: base,
		end:  end,
		ok:   true,
	}
}

// openMachO parses a thin Mach-O executable, or the slice matching the
// running architecture from a universal (fat) binary such as a lipo'd release
// build. The reader must stay valid for the returned file's lifetime.
func openMachO(r io.ReaderAt) (*macho.File, error) {
	file, err := macho.NewFile(r)
	if err == nil {
		return file, nil
	}

	fat, fatErr := macho.NewFatFile(r)
	if fatErr != nil {
		return nil, err
	}
	var want macho.Cpu
	switch runtime.GOARCH {
	case "amd64":
		want = macho.CpuAmd64
	case "arm64":
		want = macho.CpuArm64
	case "386":
		want = macho.Cpu386
	default:
		return nil, err
	}
	for _, arch := range fat.Arches {
		if arch.Cpu == want {
			return arch.File, nil
		}
	}
	return nil, err
}

// machoUUID extracts the LC_UUID load command payload.
func machoUUID(file *macho.File) ([16]byte, bool) {
	const lcUUID = 0x1b
	var uuid [16]byte
	for _, load := range file.Loads {
		raw := load.Raw()
		// Load command layout: cmd uint32, cmdsize uint32, payload
		if len(raw) >= 24 && binary.LittleEndian.Uint32(raw[0:4]) == lcUUID {
			copy(uuid[:], raw[8:24])
			return uuid, true
		}
	}
	return uuid, false
}

// machoSlide computes the ASLR slide by comparing the runtime address of a
// function in this package with its link-time address from the Go pclntab
// (the Mach-O symbol table no longer carries Go function symbols). The pinned
// handle guarantees the file is the mapped binary, so one reference point is
// sound.
func machoSlide(file *macho.File) (uint64, bool) {
	pclntab := file.Section("__gopclntab")
	text := file.Section("__text")
	if pclntab == nil || text == nil {
		return 0, false
	}
	data, err := pclntab.Data()
	if err != nil {
		return 0, false
	}
	table, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return 0, false
	}

	entry := runtime.FuncForPC(funcPC()).Entry()
	fn := table.LookupFunc(runtime.FuncForPC(entry).Name())
	if fn == nil || uint64(entry) < fn.Entry {
		return 0, false
	}
	return uint64(entry) - fn.Entry, true
}

// funcPC returns a program counter inside this package, used as the slide
// reference point.
//
//go:noinline
func funcPC() uintptr {
	pcs := make([]uintptr, 1)
	runtime.Callers(1, pcs)
	return pcs[0]
}
