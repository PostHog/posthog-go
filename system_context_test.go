package posthog

import (
	"runtime"
	"testing"
)

func TestGetOSName(t *testing.T) {
	name := getOSName()

	expected := map[string]string{
		"darwin":  "Mac OS X",
		"linux":   "Linux",
		"windows": "Windows",
		"freebsd": "FreeBSD",
	}

	if want, ok := expected[runtime.GOOS]; ok {
		if name != want {
			t.Errorf("getOSName() = %q, want %q", name, want)
		}
	} else if name != runtime.GOOS {
		t.Errorf("getOSName() = %q, want %q", name, runtime.GOOS)
	}
}

func TestSystemContext(t *testing.T) {
	ctx := systemContext()

	if ctx["$os"] != getOSName() {
		t.Errorf("systemContext()[\"$os\"] = %q, want %q", ctx["$os"], getOSName())
	}
}
