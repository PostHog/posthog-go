//go:build windows

package posthog

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func getOSInfo() osInfo {
	ver := windows.RtlGetVersion()
	return osInfo{
		Name:    "Windows",
		Version: fmt.Sprintf("%d.%d.%d", ver.MajorVersion, ver.MinorVersion, ver.BuildNumber),
	}
}
