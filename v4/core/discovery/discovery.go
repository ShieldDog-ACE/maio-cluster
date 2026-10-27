// Package discovery finds a coordinator on the local network.
//
// A signed UDP beacon is sent to every interface's directed broadcast address
// and to 255.255.255.255. All four maio transports (LAN, cable, USB tethering,
// hotspot) are IP links, so the same beacon works on each. The token never
// leaves the machine: only an HMAC of the beacon is transmitted.
package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// DefaultPortOffset is added to the cluster HTTP port when no discovery port is
// configured.
const DefaultPortOffset = 1

// Beacon advertises one coordinator.
type Beacon struct {
	Version   int    `json:"v"`
	Protocol  int    `json:"p"`
	ClusterID string `json:"c"`
	Name      string `json:"n"`
	Host      string `json:"h"`
	Port      int    `json:"a"`
	Timestamp int64  `json:"t"`
	Sig       string `json:"s"`
}

// Port returns the discovery UDP port for an API port.
func Port(apiPort int) int {
	if p := envPort(); p != 0 {
		return p
	}
	return apiPort + DefaultPortOffset
}

func envPort() int {
	// The environment override keeps multiple agents testable on one host.
	if v, ok := lookupEnv("MAIO_DISCOVERY_PORT"); ok {
		n, _ := strconv.Atoi(v)
		if n > 0 && n < 65536 {
			return n
		}
	}
	return 0
}

// Listen starts a UDP listener and returns verified beacons.
func Listen(ctx context.Context, token string, port int) (<-chan Beacon, error) {
	pc, err := net.ListenPacket("udp4", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("cannot listen for discovery beacons on udp/%d: %w", port, err)
	}
	ch := make(chan Beacon, 8)
	go func() {
		defer close(ch)
		defer pc.Close()
		go func() {
			<-ctx.Done()
			pc.Close()
		}()
		buf := make([]byte, 4096)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var b Beacon
			if err := json.Unmarshal(buf[:n], &b); err != nil {
				continue
			}
			if b.Protocol != version.Protocol || b.Version != 1 {
				continue
			}
			if time.Since(time.Unix(b.Timestamp, 0)) > 15*time.Second {
				continue
			}
			if !verify(token, b) {
				continue
			}
			select {
			case ch <- b:
			default:
			}
		}
	}()
	return ch, nil
}

// Broadcast sends beacons until ctx is done.
func Broadcast(ctx context.Context, token string, b Beacon, port int) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteBuffer(64 << 10)
	_ = conn.SetReadBuffer(1 << 10)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		b.Timestamp = time.Now().Unix()
		b.Version = 1
		b.Protocol = version.Protocol
		b.Sig = sign(token, b)
		payload, err := json.Marshal(b)
		if err == nil {
			targets := map[string]bool{}
			for _, target := range netinfo.BroadcastTargets() {
				if targets[target] {
					continue
				}
				targets[target] = true
				addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(target, strconv.Itoa(port)))
				if err != nil {
					continue
				}
				_, _ = conn.WriteToUDP(payload, addr)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Find waits for the first valid beacon.
func Find(ctx context.Context, token string, port int, timeout time.Duration) (Beacon, error) {
	ch, err := Listen(ctx, token, port)
	if err != nil {
		return Beacon{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return Beacon{}, fmt.Errorf("discovery listener closed")
			}
			return b, nil
		case <-t.C:
			return Beacon{}, fmt.Errorf("no coordinator beacon heard on udp/%d within %s", port, timeout)
		case <-ctx.Done():
			return Beacon{}, ctx.Err()
		}
	}
}

func sign(token string, b Beacon) string {
	mac := hmac.New(sha256.New, []byte(token))
	fmt.Fprintf(mac, "%d|%d|%s|%s|%s|%d|%d", b.Version, b.Protocol, b.ClusterID, b.Name, b.Host, b.Port, b.Timestamp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verify(token string, b Beacon) bool {
	want := sign(token, b)
	return hmac.Equal([]byte(want), []byte(b.Sig))
}
