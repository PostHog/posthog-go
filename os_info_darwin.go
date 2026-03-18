//go:build darwin

package posthog

import (
	"os/exec"
	"strings"
)

func getOSInfo() osInfo {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return osInfo{
			Name: "Mac OS X",
		}
	}
	return osInfo{
		Name:    "Mac OS X",
		Version: strings.TrimSpace(string(out)),
	}
}
