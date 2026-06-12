//go:build darwin

package posthog

import (
	"debug/gosym"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// loadMainImage inspects the running executable: its Mach-O UUID (which
// `posthog-cli dsym upload` uses, uppercase, as the symbol set id) and its
// runtime load address, computed from the ASLR slide of a known function.
func loadMainImage() mainImageInfo {
	exe, err := os.Executable()
	if err != nil {
		return mainImageInfo{}
	}

	file, err := macho.Open(exe)
	if err != nil {
		return mainImageInfo{}
	}
	defer file.Close()

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
			CodeFile:    exe,
			Arch:        nativeImageArch(),
		},
		base: base,
		end:  end,
		ok:   true,
	}
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
// (the Mach-O symbol table no longer carries Go function symbols).
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

	pc := runtime.FuncForPC(funcPC()).Entry()
	fn := table.LookupFunc(runtime.FuncForPC(pc).Name())
	if fn == nil || uint64(pc) < fn.Entry {
		return 0, false
	}
	return uint64(pc) - fn.Entry, true
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
