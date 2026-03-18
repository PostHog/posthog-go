package posthog

import (
	"runtime"
	"testing"
)

func TestGetOSInfo(t *testing.T) {
	info := getOSInfo()

	expectedNames := map[string]string{
		"darwin":  "Mac OS X",
		"linux":   "Linux",
		"windows": "Windows",
		"freebsd": "FreeBSD",
		"android": "Android",
		"ios":     "iOS",
	}

	if want, ok := expectedNames[runtime.GOOS]; ok {
		if info.Name != want {
			t.Errorf("getOSInfo().Name = %q, want %q", info.Name, want)
		}
	} else if info.Name != runtime.GOOS {
		t.Errorf("getOSInfo().Name = %q, want %q", info.Name, runtime.GOOS)
	}

	switch runtime.GOOS {
	case "linux":
		if info.Version == "" {
			t.Errorf("getOSInfo().Version returned empty string on linux")
		}
		if info.Distro == "" {
			t.Errorf("getOSInfo().Distro returned empty string on linux")
		}
	case "darwin", "windows":
		if info.Version == "" {
			t.Errorf("getOSInfo().Version returned empty string on %s", runtime.GOOS)
		}
		if info.Distro != "" {
			t.Errorf("getOSInfo().Distro = %q, want empty string on %s", info.Distro, runtime.GOOS)
		}
	default:
		if info.Version != "" {
			t.Errorf("getOSInfo().Version = %q, want empty string on %s", info.Version, runtime.GOOS)
		}
		if info.Distro != "" {
			t.Errorf("getOSInfo().Distro = %q, want empty string on %s", info.Distro, runtime.GOOS)
		}
	}
}

func TestGetSystemContext(t *testing.T) {
	ctx := getSystemContext()

	if ctx.Name != getOSInfo().Name {
		t.Errorf("getSystemContext().Name = %q, want %q", ctx.Name, getOSInfo().Name)
	}

	if ctx.Version != getOSInfo().Version {
		t.Errorf("getSystemContext().Version = %q, want %q", ctx.Version, getOSInfo().Version)
	}

	if ctx.GoVersion != runtime.Version() {
		t.Errorf("getSystemContext().GoVersion = %q, want %q", ctx.GoVersion, runtime.Version())
	}

	if runtime.GOOS == "linux" {
		if ctx.Distro == "" {
			t.Errorf("getSystemContext().Distro should be set on linux")
		}
	} else {
		if ctx.Distro != "" {
			t.Errorf("getSystemContext().Distro = %q, want empty on %s", ctx.Distro, runtime.GOOS)
		}
	}
}

func TestSystemContextToProperties(t *testing.T) {
	ctx := getSystemContext()
	props := ctx.ToProperties()

	if props["$os"] != ctx.Name {
		t.Errorf("MergeStruct $os = %q, want %q", props["$os"], ctx.Name)
	}

	if props["$go_version"] != ctx.GoVersion {
		t.Errorf("MergeStruct $go_version = %q, want %q", props["$go_version"], ctx.GoVersion)
	}

	if runtime.GOOS == "linux" {
		if props["$os_version"] == nil || props["$os_version"] == "" {
			t.Errorf("MergeStruct $os_version should be set on linux")
		}
		if props["$os_distro"] == nil || props["$os_distro"] == "" {
			t.Errorf("MergeStruct $os_distro should be set on linux")
		}
	} else if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if props["$os_version"] == nil || props["$os_version"] == "" {
			t.Errorf("MergeStruct $os_version should be set on %s", runtime.GOOS)
		}
		if _, exists := props["$os_distro"]; exists {
			t.Errorf("MergeStruct $os_distro should not be set on %s", runtime.GOOS)
		}
	} else {
		if _, exists := props["$os_version"]; exists {
			t.Errorf("MergeStruct $os_version should not be set on %s", runtime.GOOS)
		}
		if _, exists := props["$os_distro"]; exists {
			t.Errorf("MergeStruct $os_distro should not be set on %s", runtime.GOOS)
		}
	}
}
