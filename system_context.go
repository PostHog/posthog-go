package posthog

import "runtime"

var osNameMap = map[string]string{
	"windows": "Windows",
	"darwin":  "Mac OS X",
	"linux":   "Linux",
	"freebsd": "FreeBSD",
}

func getOSName() string {
	if name, ok := osNameMap[runtime.GOOS]; ok {
		return name
	}
	return runtime.GOOS
}

func systemContext() Properties {
	return Properties{
		"$os": getOSName(),
	}
}
