//go:build windows

package machineid

import (
	"os/exec"
	"strings"
)

func rawID() string {
	out, err := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 3 {
		return fields[len(fields)-1]
	}
	return ""
}
