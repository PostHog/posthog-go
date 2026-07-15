//go:build linux

package posthog

import "testing"

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
