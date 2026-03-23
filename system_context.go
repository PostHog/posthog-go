package posthog

import "runtime"

type osInfo struct {
	Name    string `json:"$os"`
	Version string `json:"$os_version,omitempty"`
	Distro  string `json:"$os_distro,omitempty"`
}

type sysContext struct {
	osInfo
	GoVersion string `json:"$go_version"`
}

func getSystemContext() sysContext {
	return sysContext{
		osInfo:    getOSInfo(),
		GoVersion: runtime.Version(),
	}
}

func (ctx sysContext) ToProperties() Properties {
	props := Properties{
		"$os":         ctx.Name,
		"$go_version": ctx.GoVersion,
	}
	if ctx.Version != "" {
		props["$os_version"] = ctx.Version
	}
	if ctx.Distro != "" {
		props["$os_distro"] = ctx.Distro
	}
	return props
}
