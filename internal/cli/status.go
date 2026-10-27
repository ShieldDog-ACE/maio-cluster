package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/systemd"
)

type apiStats struct {
	Metrics struct {
		Version           string            `json:"version"`
		UptimeSeconds     int64             `json:"uptime_seconds"`
		PID               int               `json:"pid"`
		Goroutines        int               `json:"goroutines"`
		RSSBytes          uint64            `json:"rss_bytes"`
		VirtualBytes      uint64            `json:"virtual_bytes"`
		OpenFDs           int               `json:"open_fds"`
		MemoryLimitBytes  int64             `json:"memory_limit_bytes"`
		HeapAllocBytes    uint64            `json:"heap_alloc_bytes"`
		ActiveRequests    int64             `json:"active_requests"`
		QueueDepth        int64             `json:"queue_depth"`
		QueueCapacity     int64             `json:"queue_capacity"`
		LoadedModels      int64             `json:"loaded_models"`
		ResidentModels    int64             `json:"resident_models"`
		RequestsCompleted uint64            `json:"requests_completed"`
		AuthFailures      uint64            `json:"auth_failures"`
		RateLimited       uint64            `json:"rate_limited"`
		QueueRejected     uint64            `json:"queue_rejected"`
		QueueTimeouts     uint64            `json:"queue_timeouts"`
		EngineRestarts    uint64            `json:"engine_restarts"`
		RestartFailures   uint64            `json:"engine_restart_failures"`
		Panics            uint64            `json:"panics"`
		TokensIn          uint64            `json:"tokens_in"`
		TokensOut         uint64            `json:"tokens_out"`
		BytesStreamed     uint64            `json:"bytes_streamed"`
		RequestsByRoute   map[string]uint64 `json:"requests_by_route"`
	} `json:"metrics"`
	Service struct {
		Version        string `json:"version"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		PID            int    `json:"pid"`
		MemoryLimitMB  int    `json:"memory_limit_mb"`
		MaxConcurrency int    `json:"max_concurrency"`
		MaxQueue       int    `json:"max_queue"`
		Parallel       int    `json:"parallel"`
		KVType         string `json:"kv_type"`
		IdleUnload     int    `json:"idle_unload"`
	} `json:"service"`
	Auth struct {
		Required bool   `json:"required"`
		Source   string `json:"source"`
		Loopback bool   `json:"loopback"`
	} `json:"auth"`
	Models []struct {
		Name       string    `json:"name"`
		State      string    `json:"state"`
		Port       int       `json:"port"`
		PID        int       `json:"pid"`
		Context    uint32    `json:"context"`
		Parallel   int       `json:"parallel"`
		KVType     string    `json:"kv_type"`
		VRAMBytes  uint64    `json:"vram_bytes"`
		Started    string    `json:"started"`
		LastUsed   time.Time `json:"last_used"`
		Resident   bool      `json:"resident"`
		Active     int       `json:"active"`
		ExpiresAt  string    `json:"expires_at"`
		RestartTry int       `json:"restart_attempts"`
	} `json:"models"`
	Resident []string `json:"resident"`
}

func fetchStats(conf *config.Global, timeout time.Duration) (*apiStats, error) {
	client := newAPIClient(conf)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var st apiStats
	if err := client.get(ctx, "/api/stats", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func bootHint(conf *config.Global) string {
	if conf.Scope == "system" {
		return "sudo maio --system boot 1"
	}
	return "maio boot 1"
}

func serviceMode(conf *config.Global) (systemd.Mode, bool) {
	if conf.Scope == "system" {
		st := systemd.Query(systemd.System)
		return systemd.System, st.Exists || st.Enabled || st.Active
	}
	st := systemd.Query(systemd.User)
	return systemd.User, st.Exists || st.Enabled || st.Active
}

func cmdStatus(conf *config.Global, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio status [--json]                 user service (default)")
			fmt.Println("       sudo maio --system status [--json]    system service")
			return 0
		case "--json":
			asJSON = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	mode, installed := serviceMode(conf)
	var selected systemd.Status
	if mode == systemd.System {
		selected = systemd.Query(systemd.System)
	} else {
		selected = systemd.Query(systemd.User)
	}
	stats, statsErr := fetchStats(conf, 3*time.Second)

	reachable := statsErr == nil
	report := map[string]any{
		"version":      Version,
		"service_mode": string(mode),
		"installed":    installed,
		"scope":        string(mode),
		"systemd": map[string]any{
			"exists": selected.Exists, "enabled": selected.Enabled, "active": selected.Active,
		},
		"reachable": reachable,
		"endpoint":  newAPIClient(conf).label,
	}
	if statsErr != nil {
		report["error"] = statsErr.Error()
	} else {
		report["stats"] = stats
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(report)
		if !reachable {
			return errs.ExitCode(errs.New(errs.Service, "service is not reachable"))
		}
		return 0
	}

	fmt.Printf("maio %s\n", Version)
	state := serviceState(selected)
	fmt.Printf("  scope     : %s (default is user; --system selects /etc/maio)\n", mode)
	fmt.Printf("  service   : %s (%s service, installed=%v)\n", state, mode, installed)
	if reachable {
		fmt.Printf("  health    : ok at %s\n", newAPIClient(conf).label)
		fmt.Printf("  uptime    : %s (pid %d)\n", formatUptime(stats.Metrics.UptimeSeconds), stats.Metrics.PID)
		fmt.Printf("  memory    : %s RSS (limit %s)\n", humanBytes(stats.Metrics.RSSBytes), humanLimit(stats.Metrics.MemoryLimitBytes))
		fmt.Printf("  requests  : %d completed, %d active, queue %d/%d\n",
			stats.Metrics.RequestsCompleted, stats.Metrics.ActiveRequests,
			stats.Metrics.QueueDepth, stats.Metrics.QueueCapacity)
		fmt.Printf("  models    : %d loaded, %d resident\n", stats.Metrics.LoadedModels, stats.Metrics.ResidentModels)
		authState := "disabled"
		if stats.Auth.Required {
			authState = "required (" + stats.Auth.Source + ")"
		}
		fmt.Printf("  auth      : %s\n", authState)
	} else {
		var ae *apiError
		if errors.As(statsErr, &ae) && ae.Status == 401 {
			fmt.Printf("  health    : unauthorized (the service is up, but the key was rejected)\n")
			fmt.Printf("  hint      : read the key with: maio token --show\n")
		} else if errors.As(statsErr, &ae) && ae.Status == 404 {
			restart := "maio restart"
			if conf.Scope == "system" {
				restart = "sudo maio --system restart"
			}
			fmt.Printf("  health    : the running service is older and has no /api/stats\n")
			fmt.Printf("  hint      : install 3.1.0, then: %s\n", restart)
		} else {
			fmt.Printf("  health    : unreachable (%v)\n", statsErr)
			fmt.Printf("  hint      : start it with: maio service start\n")
		}
	}
	if !reachable {
		return errs.ExitCode(errs.New(errs.Service, "service is not reachable"))
	}
	return 0
}

func serviceState(st systemd.Status) string {
	if st.Active {
		return "active"
	}
	if st.Exists {
		return "inactive"
	}
	return "not installed"
}

func cmdStats(conf *config.Global, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio stats [--json]")
			return 0
		case "--json":
			asJSON = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	stats, err := fetchStats(conf, 10*time.Second)
	if err != nil {
		return printAPIError(err)
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(stats)
		return 0
	}
	m := stats.Metrics
	fmt.Printf("maio %s  uptime %s  pid %d\n", m.Version, formatUptime(m.UptimeSeconds), m.PID)
	fmt.Printf("control plane : %s RSS, %s virtual, %d fds, %d goroutines, limit %s\n",
		humanBytes(m.RSSBytes), humanBytes(m.VirtualBytes), m.OpenFDs, m.Goroutines, humanLimit(m.MemoryLimitBytes))
	fmt.Printf("traffic       : %d requests, %d active, queue %d/%d, %d queued rejected, %d queue timeouts\n",
		m.RequestsCompleted, m.ActiveRequests, m.QueueDepth, m.QueueCapacity, m.QueueRejected, m.QueueTimeouts)
	fmt.Printf("security      : %d auth failures, %d rate limited, %d panics\n", m.AuthFailures, m.RateLimited, m.Panics)
	fmt.Printf("engines       : %d restarts, %d restart failures, %d loaded, %d resident\n",
		m.EngineRestarts, m.RestartFailures, m.LoadedModels, m.ResidentModels)
	fmt.Printf("tokens        : %d in, %d out, %s streamed\n", m.TokensIn, m.TokensOut, humanBytes(m.BytesStreamed))
	if m.RSSBytes > 200<<20 {
		fmt.Printf("WARN          : control plane RSS %s exceeds the 200 MiB target\n", humanBytes(m.RSSBytes))
	}
	if len(stats.Models) == 0 {
		fmt.Println("\n(no models loaded)")
		return 0
	}
	fmt.Printf("\n%-24s %-6s %6s %7s %6s %8s %-5s %s\n", "NAME", "STATE", "PORT", "PID", "SLOTS", "VRAM", "PIN", "EXPIRES")
	for _, mo := range stats.Models {
		expires := "resident"
		if !mo.Resident {
			expires = mo.ExpiresAt
			if expires == "" {
				expires = "-"
			}
		}
		fmt.Printf("%-24s %-6s %6d %7d %6d %8s %-5v %s\n", mo.Name, mo.State, mo.Port, mo.PID,
			mo.Parallel, humanBytes(mo.VRAMBytes), mo.Resident, expires)
	}
	return 0
}

func cmdService(conf *config.Global, args []string) int {
	action := ""
	tail := 100
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: maio service <start|stop|restart|status|enable|disable|logs> [--tail N]")
			fmt.Println("       sudo maio --system service <action>     system service")
			return 0
		case "--tail":
			if i+1 >= len(args) {
				return usagef("--tail requires a number")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return usagef("invalid --tail value %q", args[i])
			}
			tail = n
		default:
			if strings.HasPrefix(args[i], "-") {
				return usagef("unknown argument %s", args[i])
			}
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		return usagef("usage: maio service <start|stop|restart|status|enable|disable|logs>")
	}
	action = rest[0]
	if action == "enable" {
		return cmdBoot(conf, []string{"1"})
	}
	if action == "disable" {
		return cmdBoot(conf, []string{"0"})
	}
	mode, installed := serviceMode(conf)
	if !installed {
		return fail(errs.New(errs.Service, "maio is not installed as a service").
			WithHint("enable it with: %s", bootHint(conf)))
	}
	switch action {
	case "status":
		st := systemd.Query(mode)
		fmt.Printf("%s service: active=%v enabled=%v unit=%s\n", mode, st.Active, st.Enabled, systemd.UnitPath(mode))
		if !st.Active {
			return errs.ExitCode(errs.New(errs.Service, "the service is not running"))
		}
		return 0
	case "start", "stop", "restart", "reload":
		if err := systemd.Control(mode, action); err != nil {
			return fail(err)
		}
		done := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted", "reload": "reloaded"}[action]
		fmt.Printf("%s the %s maio service\n", done, mode)
		return 0
	case "logs":
		out, err := systemd.Journal(mode, tail)
		if err != nil {
			return fail(err)
		}
		if strings.TrimSpace(out) == "" {
			fmt.Println("(no journal entries)")
			return 0
		}
		fmt.Println(out)
		return 0
	default:
		return usagef("unknown service action %q", action)
	}
}

func cmdRestart(conf *config.Global, args []string) int {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio restart                       user service (default)")
			fmt.Println("       sudo maio --system restart        system service")
			return 0
		default:
			return usagef("unknown argument %s", a)
		}
	}
	mode, installed := serviceMode(conf)
	if !installed {
		return fail(errs.New(errs.Service, "maio is not installed as a service").
			WithHint("enable it with: %s", bootHint(conf)))
	}
	if err := systemd.Control(mode, "restart"); err != nil {
		return fail(err)
	}
	fmt.Printf("restarted the %s maio service\n", mode)
	logging.Info("service.restart", "mode", string(mode))
	return 0
}

func formatUptime(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func humanBytes(n uint64) string {
	return hardware.HumanBytes(n)
}

func humanLimit(limit int64) string {
	if limit <= 0 {
		return "unlimited"
	}
	return hardware.HumanBytes(uint64(limit))
}
