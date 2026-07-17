//go:build linux

package posthog

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"
)

// TestGNUBuildIDFromProgramHeader covers fully stripped binaries: no section
// table at all, with the NT_GNU_BUILD_ID note reachable only through a
// PT_NOTE program header. The ELF is hand-assembled: file header, one program
// header, then the note.
func TestGNUBuildIDFromProgramHeader(t *testing.T) {
	le := binary.LittleEndian
	buildID := []byte{
		0x55, 0x53, 0x98, 0xeb, 0xd0, 0x1c, 0x90, 0x28, 0x5a, 0x3d,
		0x85, 0x13, 0x8a, 0x19, 0xcb, 0xf9, 0xbb, 0xce, 0xc3, 0x52,
	}

	note := make([]byte, 12, 12+4+len(buildID))
	le.PutUint32(note[0:4], 4)                    // namesz ("GNU\x00")
	le.PutUint32(note[4:8], uint32(len(buildID))) // descsz
	le.PutUint32(note[8:12], 3)                   // NT_GNU_BUILD_ID
	note = append(note, "GNU\x00"...)
	note = append(note, buildID...)

	const ehSize, phSize = 64, 56
	image := make([]byte, ehSize+phSize+len(note))
	copy(image, "\x7fELF")
	image[4] = 2                     // ELFCLASS64
	image[5] = 1                     // little-endian
	image[6] = 1                     // EV_CURRENT
	le.PutUint16(image[16:18], 2)    // ET_EXEC
	le.PutUint16(image[18:20], 0x3e) // EM_X86_64
	le.PutUint32(image[20:24], 1)    // e_version
	le.PutUint64(image[32:40], ehSize)
	le.PutUint16(image[52:54], ehSize)
	le.PutUint16(image[54:56], phSize)
	le.PutUint16(image[56:58], 1) // e_phnum

	ph := image[ehSize:]
	le.PutUint32(ph[0:4], uint32(elf.PT_NOTE))
	le.PutUint32(ph[4:8], 4) // PF_R
	le.PutUint64(ph[8:16], ehSize+phSize)
	le.PutUint64(ph[32:40], uint64(len(note))) // p_filesz
	le.PutUint64(ph[40:48], uint64(len(note))) // p_memsz
	le.PutUint64(ph[48:56], 4)                 // p_align
	copy(image[ehSize+phSize:], note)

	file, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if len(file.Sections) != 0 {
		t.Fatal("fixture must have no section table")
	}

	if got := gnuBuildID(file); !bytes.Equal(got, buildID) {
		t.Errorf("gnuBuildID() = %x, want %x", got, buildID)
	}
}

func TestMapsLineRange(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		exe   string
		start uint64
		stop  uint64
		ok    bool
	}{
		{
			name:  "plain mapping",
			line:  "00400000-0047f000 r-xp 00000000 fd:01 123456                             /usr/bin/app",
			exe:   "/usr/bin/app",
			start: 0x400000,
			stop:  0x47f000,
			ok:    true,
		},
		{
			name:  "path with spaces",
			line:  "00400000-0047f000 r-xp 00000000 fd:01 123456                             /opt/my app/bin server",
			exe:   "/opt/my app/bin server",
			start: 0x400000,
			stop:  0x47f000,
			ok:    true,
		},
		{
			name:  "deleted executable",
			line:  "00400000-0047f000 r-xp 00000000 fd:01 123456                             /usr/bin/app (deleted)",
			exe:   "/usr/bin/app",
			start: 0x400000,
			stop:  0x47f000,
			ok:    true,
		},
		{
			name: "different file",
			line: "7f0000000000-7f0000001000 r-xp 00000000 fd:01 654321                     /usr/lib/libc.so.6",
			exe:  "/usr/bin/app",
			ok:   false,
		},
		{
			name: "anonymous mapping",
			line: "7ffd1c000000-7ffd1c021000 rw-p 00000000 00:00 0                          [stack]",
			exe:  "/usr/bin/app",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, stop, ok := mapsLineRange(tt.line, tt.exe)
			if ok != tt.ok || start != tt.start || stop != tt.stop {
				t.Errorf("mapsLineRange() = (%#x, %#x, %v), want (%#x, %#x, %v)",
					start, stop, ok, tt.start, tt.stop, tt.ok)
			}
		})
	}
}
