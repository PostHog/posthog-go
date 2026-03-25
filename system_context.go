package posthog

import (
	"runtime"
	"sync"
)

type osInfo struct {
	Name    string `json:"$os"`
	Version string `json:"$os_version,omitempty"`
	Distro  string `json:"$os_distro,omitempty"`
}

type sysContext struct {
	osInfo
	GoVersion string `json:"$go_version"`
}

// cachedSystemContext holds the cached system context, computed once per process.
// OS info and Go version don't change during runtime, so there's no need to
// re-compute (and re-exec sw_vers/uname) on every event.
var (
	cachedSystemContext     sysContext
	cachedSystemContextOnce sync.Once
	// cachedSysContextProps holds the pre-built properties from the cached system context.
	// This avoids creating a new Properties map on every ToProperties() call.
	cachedSysContextProps     Properties
	cachedSysContextPropsOnce sync.Once
)

func getSystemContext() sysContext {
	cachedSystemContextOnce.Do(func() {
		cachedSystemContext = sysContext{
			osInfo:    getOSInfo(),
			GoVersion: runtime.Version(),
		}
	})
	return cachedSystemContext
}

func (ctx sysContext) ToProperties() Properties {
	cachedSysContextPropsOnce.Do(func() {
		sc := getSystemContext()
		props := Properties{
			"$os":         sc.Name,
			"$go_version": sc.GoVersion,
		}
		if sc.Version != "" {
			props["$os_version"] = sc.Version
		}
		if sc.Distro != "" {
			props["$os_distro"] = sc.Distro
		}
		cachedSysContextProps = props
	})
	return cachedSysContextProps
}
