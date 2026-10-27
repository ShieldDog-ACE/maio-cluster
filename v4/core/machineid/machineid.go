// Package machineid derives a stable, non-reversible local machine identifier.
//
// The raw platform identifier is never exposed or logged: maio hashes it and
// prints a short "mid_..." prefix. The identifier is stable across restarts and
// independent of the cluster membership, so it can identify a machine before it
// has joined any cluster.
package machineid

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"strings"
)

// ID returns a stable identifier such as "mid_9f2c1a7b4d6e8f01".
func ID() string {
	raw := strings.TrimSpace(rawID())
	if raw == "" {
		raw = fallbackSeed()
	}
	sum := sha256.Sum256([]byte("maio-machineid-v1:" + raw))
	return "mid_" + hex.EncodeToString(sum[:8])
}

// Hash is exposed for tests and diagnostics.
func Hash(seed string) string {
	sum := sha256.Sum256([]byte("maio-machineid-v1:" + seed))
	return "mid_" + hex.EncodeToString(sum[:8])
}

func fallbackSeed() string {
	host, _ := os.Hostname()
	mac := ""
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) == 0 {
				continue
			}
			mac = ifc.HardwareAddr.String()
			break
		}
	}
	return runtime.GOOS + "|" + runtime.GOARCH + "|" + host + "|" + mac
}
