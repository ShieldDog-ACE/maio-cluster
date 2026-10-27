//go:build darwin

package machineid

import (
	"os/exec"
	"regexp"
	"strings"
)

var platformUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

func rawID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err == nil {
		if m := platformUUID.FindSubmatch(out); len(m) == 2 {
			return strings.TrimSpace(string(m[1]))
		}
	}
	return ""
}
