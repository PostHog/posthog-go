//go:build !linux && !darwin && !windows

package posthog

import "runtime"

var osNameMap = map[string]string{
	"freebsd": "FreeBSD",
	"android": "Android",
	"ios":     "iOS",
}

func getOSInfo() osInfo {
	name := runtime.GOOS
	if mapped, ok := osNameMap[runtime.GOOS]; ok {
		name = mapped
	}
	return osInfo{
		Name: name,
	}
}
