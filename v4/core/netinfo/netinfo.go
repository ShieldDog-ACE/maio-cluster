// Package netinfo classifies the local links maio can distribute work over.
//
// All four supported transports are IP links, so the cluster protocol does not
// change between them:
//
//	lan       Wi-Fi or switched LAN
//	ethernet  wired LAN, including a direct cable between two machines
//	usb       USB tethering / RNDIS / adb reverse
//	hotspot   phone or travel-router access point
//
// Classification is advisory. Discovery and RPC always use real interface
// addresses, never a guessed link name.
package netinfo

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// LinkKind is a coarse transport label.
type LinkKind string

const (
	Loopback LinkKind = "loopback"
	Ethernet LinkKind = "ethernet"
	WiFi     LinkKind = "wifi"
	USB      LinkKind = "usb"
	Hotspot  LinkKind = "hotspot"
	Virtual  LinkKind = "virtual"
	Other    LinkKind = "other"
)

// Address is one interface address.
type Address struct {
	Interface string   `json:"interface"`
	IP        string   `json:"ip"`
	Prefix    int      `json:"prefix"`
	Network   string   `json:"network"`
	Broadcast string   `json:"broadcast,omitempty"`
	Kind      LinkKind `json:"kind"`
	Private   bool     `json:"private"`
	LinkLocal bool     `json:"link_local"`
	Up        bool     `json:"up"`
	MTU       int      `json:"mtu"`
}

// Snapshot lists usable IPv4 addresses.
func Snapshot() ([]Address, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Address
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			kind := classify(ifc.Name, ip4, ones)
			out = append(out, Address{
				Interface: ifc.Name,
				IP:        ip4.String(),
				Prefix:    ones,
				Network:   (&net.IPNet{IP: ip4.Mask(ipnet.Mask), Mask: ipnet.Mask}).String(),
				Broadcast: broadcast(ip4, ipnet.Mask),
				Kind:      kind,
				Private:   ip4.IsPrivate() || kind == Hotspot,
				LinkLocal: ip4[0] == 169 && ip4[1] == 254,
				Up:        true,
				MTU:       ifc.MTU,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i].Interface < out[j].Interface
	})
	return out, nil
}

func rank(a Address) int {
	if a.IP == "127.0.0.1" || a.LinkLocal || a.Kind == Loopback {
		return 90
	}
	switch a.Kind {
	case Ethernet:
		return 10
	case USB:
		return 12
	case WiFi:
		return 20
	case Hotspot:
		return 25
	case Virtual:
		return 60
	default:
		return 40
	}
}

func classify(name string, ip net.IP, prefix int) LinkKind {
	n := strings.ToLower(name)
	if ip.IsLoopback() || n == "lo" || strings.HasPrefix(n, "lo") {
		return Loopback
	}
	switch {
	case strings.HasPrefix(n, "usb"), strings.HasPrefix(n, "rndis"),
		strings.Contains(n, "remote ndis"), strings.HasPrefix(n, "enx"):
		return USB
	case strings.HasPrefix(n, "wl"), strings.HasPrefix(n, "wlan"),
		strings.HasPrefix(n, "wifi"), strings.HasPrefix(n, "wi-fi"):
		return WiFi
	case strings.HasPrefix(n, "eth"), strings.HasPrefix(n, "en"),
		strings.HasPrefix(n, "eno"), strings.HasPrefix(n, "ens"),
		strings.HasPrefix(n, "enp"):
		return Ethernet
	case strings.HasPrefix(n, "tun"), strings.HasPrefix(n, "tap"),
		strings.HasPrefix(n, "veth"), strings.HasPrefix(n, "br-"),
		strings.HasPrefix(n, "docker"), strings.HasPrefix(n, "virbr"),
		strings.HasPrefix(n, "vmnet"), strings.HasPrefix(n, "utun"):
		return Virtual
	}
	// Tethering address ranges are strong hints even when the driver name is
	// opaque (for example Windows "Local Area Connection* 12").
	if prefix >= 16 {
		switch {
		case ip[0] == 192 && ip[1] == 168 && (ip[2] == 42 || ip[2] == 43 || ip[2] == 137):
			return USB
		case ip[0] == 172 && ip[1] == 20 && ip[2] == 10:
			return Hotspot
		case ip[0] == 10 && ip[1] == 42 && ip[2] == 0:
			return Hotspot
		}
	}
	return Other
}

func broadcast(ip net.IP, mask net.IPMask) string {
	if len(mask) != 4 {
		return ""
	}
	b := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		b[i] = ip[i] | ^mask[i]
	}
	return b.String()
}

// BestAdvertise picks the address peers should dial, preferring wired links
// and avoiding link-local addresses.
func BestAdvertise() (Address, bool) {
	addrs, err := Snapshot()
	if err != nil || len(addrs) == 0 {
		return Address{}, false
	}
	for _, a := range addrs {
		if a.Kind == Loopback || a.LinkLocal || a.IP == "" {
			continue
		}
		if a.Private {
			return a, true
		}
	}
	for _, a := range addrs {
		if a.Kind != Loopback && !a.LinkLocal {
			return a, true
		}
	}
	return Address{}, false
}

// BroadcastTargets returns the directed broadcast addresses of active IPv4
// links, plus the limited broadcast address as a last resort.
func BroadcastTargets() []string {
	addrs, err := Snapshot()
	if err != nil {
		return []string{"255.255.255.255"}
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		if a.Kind == Loopback || a.Broadcast == "" {
			continue
		}
		if !seen[a.Broadcast] {
			seen[a.Broadcast] = true
			out = append(out, a.Broadcast)
		}
	}
	out = append(out, "255.255.255.255")
	return out
}

// KindForIP guesses which link a peer address belongs to, for UI grouping.
func KindForIP(raw string) LinkKind {
	ip := net.ParseIP(raw).To4()
	if ip == nil {
		return Other
	}
	if ip.IsLoopback() {
		return Loopback
	}
	addrs, _ := Snapshot()
	for _, a := range addrs {
		if a.IP == ip.String() {
			return a.Kind
		}
	}
	if ip[0] == 169 && ip[1] == 254 {
		return Ethernet
	}
	return Other
}

// Describe renders one link for humans.
func (a Address) Describe() string {
	return fmt.Sprintf("%s %s/%d (%s)", a.Interface, a.IP, a.Prefix, a.Kind)
}
