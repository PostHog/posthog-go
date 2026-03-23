//go:build linux

package posthog

import (
	"os"
	"strings"
)

func getOSInfo() osInfo {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return osInfo{
			Name: "Linux",
		}
	}

	fields := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			fields[parts[0]] = strings.Trim(parts[1], `"`)
		}
	}

	return osInfo{
		Name:    "Linux",
		Version: fields["VERSION_ID"],
		Distro:  fields["NAME"],
	}
}
