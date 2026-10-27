package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func (o *options) cmdDoctor(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var checks []check
	add := func(name, status, detail, hint string) {
		checks = append(checks, check{Name: name, Status: status, Detail: detail, Hint: hint})
	}
	add("config", "ok", o.conf.Path, "")
	if o.conf.Cluster.Token == "" {
		add("token", "fail", "cluster token is empty", "run maio init --force")
	} else {
		add("token", "ok", fmt.Sprintf("%d characters", len(o.conf.Cluster.Token)), "")
	}
	if err := os.MkdirAll(o.conf.Agent.ModelsDir, 0o755); err != nil {
		add("models", "fail", err.Error(), "choose a writable agent.models_dir")
	} else if err := probeWrite(o.conf.Agent.ModelsDir); err != nil {
		add("models", "fail", err.Error(), "choose a writable agent.models_dir")
	} else {
		add("models", "ok", o.conf.Agent.ModelsDir, "")
	}
	hw := hardware.Detect(o.conf.Agent.ModelsDir)
	add("hardware", "ok", fmt.Sprintf("%s, %d cores, %s RAM, %s", hw.CPUModel, hw.CPUCores, humanBytes(hw.MemTotal), hw.DeviceSummary()), "")
	if hw.CapacityBytes() < 512<<20 {
		add("capacity", "warn", "less than 512 MiB usable for shards", "close other applications or add a GPU")
	}
	rpc := findBin(o.conf.Agent.RPCBin, []string{"ggml-rpc-server", "rpc-server"})
	if rpc == "" {
		add("rpc", "fail", "ggml-rpc-server not found", "install the maio-rpc package for this platform")
	} else {
		add("rpc", "ok", rpc, "")
	}
	llama := findBin(o.conf.Llama.ServerBin, []string{"llama-server"})
	if llama == "" {
		llama = findBin(o.conf.Agent.LlamaBin, []string{"llama-server"})
	}
	if llama == "" {
		add("llama", "warn", "llama-server not found; only remote route jobs will work", "install the maio-llama package")
	} else {
		add("llama", "ok", llama, "")
	}
	links, _ := netinfo.Snapshot()
	usable := 0
	for _, l := range links {
		if l.Kind != netinfo.Loopback && !l.LinkLocal {
			usable++
		}
	}
	if usable == 0 {
		add("link", "warn", "no non-loopback IPv4 link", "connect Ethernet, Wi-Fi, USB tethering or a hotspot")
	} else {
		var parts []string
		for _, l := range links {
			if l.Kind != netinfo.Loopback {
				parts = append(parts, l.Describe())
			}
		}
		add("link", "ok", strings.Join(parts, "; "), "")
	}
	healthCtx, hcancel := context.WithTimeout(ctx, 3*time.Second)
	err := o.client().Health(healthCtx)
	hcancel()
	if err != nil {
		add("coordinator", "warn", "not reachable: "+err.Error(), "run maio cluster up or maio service install")
	} else {
		info, _ := o.client().Info(ctx)
		add("coordinator", "ok", fmt.Sprintf("%s (%s), %d nodes", info.Name, info.ID, info.Nodes), "")
	}
	state, _ := o.adapter.ServiceStatus(o.system)
	add("service", "ok", state, "")

	failed := 0
	for _, c := range checks {
		if c.Status == "fail" {
			failed++
		}
	}
	if o.json {
		o.printJSON(map[string]any{"checks": checks, "ok": failed == 0})
	} else {
		for _, c := range checks {
			fmt.Printf("[%-4s] %-14s %s\n", c.Status, c.Name, c.Detail)
			if c.Hint != "" && c.Status != "ok" {
				fmt.Printf("       hint: %s\n", c.Hint)
			}
		}
	}
	if failed > 0 {
		return errs.ExitCode(errs.New(errs.Unavailable, "%d checks failed", failed))
	}
	return 0
}

func probeWrite(dir string) error {
	f, err := os.CreateTemp(dir, ".maio-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func findBin(configured string, names []string) string {
	if configured != "" {
		if st, err := os.Stat(configured); err == nil && !st.IsDir() {
			return configured
		}
	}
	exe, _ := os.Executable()
	candidates := []string{}
	if exe != "" {
		d := filepath.Dir(exe)
		for _, n := range names {
			candidates = append(candidates, filepath.Join(d, n), filepath.Join(d, "..", "lib", "maio-llama", "bin", n))
		}
	}
	candidates = append(candidates, names...)
	for _, c := range candidates {
		if strings.ContainsRune(c, os.PathSeparator) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func humanBytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v/1024)
}

func (o *options) cmdStatus(args []string) int {
	if o.json {
		return o.clusterStatus(args)
	}
	hw := hardware.Detect(o.conf.Agent.ModelsDir)
	fmt.Printf("maio %s (%s/%s)\n", versionString(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("config:   %s\n", o.conf.Path)
	fmt.Printf("cluster:  %s (%s), port %d\n", o.conf.Cluster.Name, o.conf.Cluster.ID, o.conf.Cluster.Port)
	fmt.Printf("hardware: %s, %d cores, %s RAM, %s\n", hw.CPUModel, hw.CPUCores, humanBytes(hw.MemTotal), hw.DeviceSummary())
	fmt.Printf("capacity: %s usable\n", humanBytes(hw.CapacityBytes()))
	if state, _ := o.adapter.ServiceStatus(o.system); state != "" {
		fmt.Printf("service:  %s\n", state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := o.client().Info(ctx)
	if err != nil {
		fmt.Printf("coordinator: unreachable (%v)\n", err)
	} else {
		fmt.Printf("coordinator: %s at %s, %d nodes\n", info.Name, o.client().BaseURL, info.Nodes)
	}
	return 0
}

func versionString() string { return version.Version }

func (o *options) cmdLink(args []string) int {
	if len(args) == 0 || args[0] == "list" {
		links, err := netinfo.Snapshot()
		if err != nil {
			errs.Print(os.Stderr, err)
			return 1
		}
		if o.json {
			return o.printJSON(map[string]any{"links": links})
		}
		fmt.Printf("%-14s %-18s %-10s %-8s %s\n", "INTERFACE", "ADDRESS", "KIND", "PRIVATE", "BROADCAST")
		for _, l := range links {
			fmt.Printf("%-14s %-18s %-10s %-8v %s\n", l.Interface, fmt.Sprintf("%s/%d", l.IP, l.Prefix), l.Kind, l.Private, l.Broadcast)
		}
		return 0
	}
	switch args[0] {
	case "usb":
		return o.linkUSB()
	case "test":
		return o.linkTest()
	case "hotspot", "wifi":
		fmt.Println("hotspot checklist:")
		fmt.Println("  1. enable the phone/travel-router hotspot")
		fmt.Println("  2. connect every node to the same SSID")
		fmt.Println("  3. run maio doctor; nodes should share a 192.168.x or 172.20.10.x subnet")
		fmt.Println("  4. if the AP isolates clients, disable AP isolation or use a cable")
		return 0
	case "cable":
		fmt.Println("direct cable checklist:")
		fmt.Println("  1. connect the Ethernet cable; both ends get 169.254.x.x or a static /24")
		fmt.Println("  2. run maio link list and confirm both nodes share a subnet")
		fmt.Println("  3. allow TCP 47800-47802 and UDP 47801 in the firewall")
		return 0
	default:
		return usageMsg("usage: maio link list|usb|test|hotspot|cable")
	}
}

func (o *options) linkUSB() int {
	adb, err := exec.LookPath("adb")
	if err != nil {
		errs.Print(os.Stderr, errs.New(errs.NotFound, "adb not found").
			WithHint("install Android platform-tools, or use Wi-Fi/hotspot/USB tethering instead"))
		return errs.ExitCode(errs.New(errs.NotFound, "adb not found"))
	}
	out, err := exec.Command(adb, "devices").CombinedOutput()
	if err != nil {
		errs.Print(os.Stderr, fmt.Errorf("adb devices: %w", err))
		return 1
	}
	port := o.conf.Cluster.Port
	devices := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "device" {
			continue
		}
		devices++
		for _, spec := range []string{
			fmt.Sprintf("tcp:%d", port),
			fmt.Sprintf("tcp:%d", port+1),
			fmt.Sprintf("tcp:%d", port+2),
		} {
			cmd := exec.Command(adb, "-s", fields[0], "reverse", spec, spec)
			if b, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "%s reverse %s: %v (%s)\n", fields[0], spec, err, strings.TrimSpace(string(b)))
			}
		}
		fmt.Printf("%s: adb reverse 127.0.0.1:%d -> coordinator\n", fields[0], port)
	}
	if devices == 0 {
		fmt.Println("no authorized Android device; enable USB debugging and authorize this computer")
	}
	fmt.Println("in the APK use http://127.0.0.1:" + fmt.Sprint(port))
	return 0
}

func (o *options) linkTest() int {
	addr := coordinatorURL(o.conf)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.client().Health(ctx); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("%s reachable via %s in %s\n", addr, netinfo.KindForIP(hostOnly(addr)), time.Since(start).Round(time.Millisecond))
	return 0
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")); err == nil {
		return h
	}
	return addr
}
