package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/auth"
	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/index"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
	"github.com/ShieldDog-ACE/maio-cluster/internal/systemd"
	"github.com/ShieldDog-ACE/maio-cluster/internal/vram"
)

type doctorCheck struct {
	Section string `json:"section"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint,omitempty"`
}

type doctorReport struct {
	Version string         `json:"version"`
	Checks  []doctorCheck  `json:"checks"`
	Summary map[string]int `json:"summary"`
}

type doctorCollector struct {
	checks []doctorCheck
}

func (c *doctorCollector) add(section, name, status, detail string, hint ...string) {
	ch := doctorCheck{Section: section, Name: name, Status: status, Detail: detail}
	if len(hint) > 0 {
		ch.Hint = hint[0]
	}
	c.checks = append(c.checks, ch)
}

func (c *doctorCollector) ok(section, name, detail string) { c.add(section, name, "ok", detail) }
func (c *doctorCollector) warn(section, name, detail string, hint ...string) {
	c.add(section, name, "warn", detail, hint...)
}
func (c *doctorCollector) fail(section, name, detail string, hint ...string) {
	c.add(section, name, "fail", detail, hint...)
}

func (c *doctorCollector) counts() (ok, warn, fail int) {
	for _, ch := range c.checks {
		switch ch.Status {
		case "ok":
			ok++
		case "warn":
			warn++
		case "fail":
			fail++
		}
	}
	return
}

func cmdDoctor(conf *config.Global, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio doctor [--json]")
			return 0
		case "--json":
			asJSON = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	c := &doctorCollector{}

	// System
	c.ok("system", "platform", fmt.Sprintf("%s/%s, %s", runtime.GOOS, runtime.GOARCH, runtime.Version()))
	c.ok("system", "os", osPrettyName())
	if kernel := commandOutput("uname", "-r"); kernel != "" {
		c.ok("system", "kernel", kernel)
	}

	// Config
	if _, err := os.Stat(conf.Source); err == nil {
		c.ok("config", "file", conf.Source)
	} else {
		c.warn("config", "file", fmt.Sprintf("%s not found; using built-in defaults", conf.Source),
			"create it with: sudo maio boot 1 (or maio boot 1 --user)")
	}
	c.ok("config", "start on boot", fmt.Sprintf("boot = %d", conf.Boot))
	c.ok("config", "models_root", conf.ModelsRoot)
	c.ok("config", "state_dir", conf.StateDir)
	if conf.RuntimeDir != conf.StateDir {
		c.warn("config", "runtime dir", fmt.Sprintf("using %s (state_dir is not writable by this user)", conf.RuntimeDir),
			"run with sudo, or set state_dir to a user-writable directory")
	}
	c.ok("config", "listen", fmt.Sprintf("%s:%d", conf.Host, conf.Port))
	c.ok("config", "context", fmt.Sprintf("ctx_size = %d", conf.CtxSize))
	c.ok("config", "mirror", nonEmpty(conf.Mirror, "(disabled)"))
	c.ok("config", "admission", fmt.Sprintf("max_concurrency = %d, max_queue = %d, queue_timeout = %ds",
		conf.MaxConcurrency, conf.MaxQueue, conf.QueueTimeout))
	c.ok("config", "engine tuning", fmt.Sprintf("parallel = %d, kv_type = %s, flash_attn = %s",
		conf.Parallel, conf.KVType, conf.FlashAttn))
	c.ok("config", "process limits", fmt.Sprintf("memory_limit_mb = %d, max_open_files = %d",
		conf.MemoryLimitMB, conf.MaxOpenFiles))
	if conf.MemoryLimitMB == 0 {
		c.warn("config", "memory limit", "memory_limit_mb = 0 (unlimited)", "set memory_limit_mb = 180 for the 200 MiB target")
	} else if conf.MemoryLimitMB > 200 {
		c.warn("config", "memory limit", fmt.Sprintf("memory_limit_mb = %d exceeds the 200 MiB target", conf.MemoryLimitMB))
	}

	// Authentication
	key, keySource, keyErr := auth.Lookup(auth.Options{
		Mode: conf.Auth, Key: conf.APIKey, KeyFile: conf.AuthKeyFile(),
		Host: conf.Host, AllowInsecure: conf.AllowInsecure == 1,
	})
	if keyErr != nil {
		c.fail("security", "api key", keyErr.Error(), errs.HintOf(keyErr))
	} else {
		switch conf.Auth {
		case "off":
			if !auth.IsLoopbackHost(conf.Host) && conf.AllowInsecure != 1 {
				c.fail("security", "auth", "auth = off on a non-loopback listener without allow_insecure = 1",
					"set auth = \"auto\", or set allow_insecure = 1 only on an isolated machine")
			} else {
				c.warn("security", "auth", "authentication is disabled", "set auth = \"auto\" to require a key")
			}
		default:
			if key == "" {
				if auth.IsLoopbackHost(conf.Host) {
					c.ok("security", "auth", "not required on a loopback listener")
				} else {
					c.warn("security", "auth", "no key yet; one will be generated on the next service start")
				}
			} else {
				c.ok("security", "auth", fmt.Sprintf("required (source: %s)", keySource))
			}
			if st, err := os.Stat(conf.AuthKeyFile()); err == nil {
				if st.Mode().Perm()&0o077 != 0 {
					c.warn("security", "key file", fmt.Sprintf("%s is mode %o", conf.AuthKeyFile(), st.Mode().Perm()),
						"run: chmod 600 "+conf.AuthKeyFile())
				} else {
					c.ok("security", "key file", fmt.Sprintf("%s (0600)", conf.AuthKeyFile()))
				}
			}
		}
	}
	if conf.TLSEnabled() {
		if _, err := os.Stat(conf.TLSCert); err != nil {
			c.fail("security", "tls cert", err.Error())
		} else if _, err := os.Stat(conf.TLSKey); err != nil {
			c.fail("security", "tls key", err.Error())
		} else {
			c.ok("security", "tls", fmt.Sprintf("enabled (%s)", conf.TLSCert))
		}
	}
	if dirExists(conf.ModelsRoot) {
		if writableDir(conf.ModelsRoot) {
			c.ok("config", "models_root access", "readable and writable")
		} else {
			c.warn("config", "models_root access", "readable but not writable",
				"model installs will need sudo, or choose a user-owned download root")
		}
	} else {
		c.warn("config", "models_root access", "directory does not exist yet")
	}

	// Hardware
	hw := hardware.Detect(conf.ModelsRoot)
	c.ok("hardware", "cpu", fmt.Sprintf("%s (%d cores)", hw.CPUModel, hw.CPUCores))
	c.ok("hardware", "memory", fmt.Sprintf("%s total, %s available",
		hardware.HumanBytes(hw.MemTotal), hardware.HumanBytes(hw.MemAvailable)))
	if hw.SwapTotal > 0 {
		c.ok("hardware", "swap", fmt.Sprintf("%s total, %s free",
			hardware.HumanBytes(hw.SwapTotal), hardware.HumanBytes(hw.SwapFree)))
	}
	c.ok("hardware", "disk", fmt.Sprintf("%s free of %s on %s",
		hardware.HumanBytes(hw.DiskFree), hardware.HumanBytes(hw.DiskTotal), hw.DiskPath))
	if hw.HasGPU() {
		total, free := hw.VRAM()
		c.ok("hardware", "gpu", fmt.Sprintf("%d NVIDIA GPU(s), %s VRAM total, %s free",
			len(hw.GPUs), hardware.HumanBytes(total), hardware.HumanBytes(free)))
		for _, g := range hw.GPUs {
			detail := fmt.Sprintf("#%d %s: %s free of %s", g.Index, g.Name,
				hardware.HumanBytes(g.MemoryFree), hardware.HumanBytes(g.MemoryTotal))
			if g.Driver != "" {
				detail += ", driver " + g.Driver
			}
			if g.ComputeCap != "" {
				detail += ", compute " + g.ComputeCap
			}
			c.ok("hardware", "gpu", detail)
		}
	} else {
		c.warn("hardware", "gpu", "no NVIDIA GPU detected; inference will run on CPU",
			"install the NVIDIA driver and nvidia-smi, then rerun doctor")
	}
	for _, w := range hw.Warnings {
		c.warn("hardware", "warning", w)
	}

	// Engine
	eng := engine.New(conf)
	bin, err := eng.BinPath()
	if err != nil {
		c.fail("engine", "llama-server", err.Error(), errs.HintOf(err))
	} else {
		c.ok("engine", "llama-server", bin)
		if v, err := eng.Version(); err == nil {
			c.ok("engine", "llama-server version", v)
		} else {
			c.warn("engine", "llama-server version", err.Error(), "the binary may be too old or built without --version")
		}
	}
	if conf.LlamaLib != "" {
		for _, p := range strings.Split(conf.LlamaLib, ":") {
			if p == "" {
				continue
			}
			if dirExists(p) {
				c.ok("engine", "llama_lib", p)
			} else {
				c.warn("engine", "llama_lib", p+" does not exist", "fix llama_lib in maio.conf")
			}
		}
	}

	// Service
	sysSt := systemd.Query(systemd.System)
	userSt := systemd.Query(systemd.User)
	serviceDetail := func(st systemd.Status) string {
		return fmt.Sprintf("unit=%s exists=%v enabled=%v active=%v", systemd.UnitPath(st.Mode), st.Exists, st.Enabled, st.Active)
	}
	if sysSt.Exists || sysSt.Enabled || sysSt.Active {
		c.ok("service", "system unit", serviceDetail(sysSt))
	} else if userSt.Enabled || userSt.Active {
		c.ok("service", "system unit", "not installed; using the per-user service")
	} else {
		c.warn("service", "system unit", "not installed", "enable it with: sudo maio boot 1")
	}
	if userSt.Exists || userSt.Enabled || userSt.Active {
		c.ok("service", "user unit", serviceDetail(userSt))
		if ok, _ := systemd.Linger(currentUser()); !ok {
			c.warn("service", "user linger", "disabled; the user service starts after login",
				"for boot-time start run: "+systemd.LingerHint(currentUser()))
		} else {
			c.ok("service", "user linger", "enabled")
		}
	}
	if conf.Boot == 1 {
		switch {
		case userSt.Enabled && userSt.Active:
			c.ok("service", "start on boot", "user service is enabled and running")
		case sysSt.Enabled && sysSt.Active:
			c.ok("service", "start on boot", "system service is enabled and running")
		case userSt.Enabled || sysSt.Enabled:
			c.fail("service", "start on boot", "boot = 1 but the enabled maio service is not running",
				"start it with: systemctl --user start maio  (or: sudo systemctl start maio); inspect with: journalctl -u maio -n 50")
		default:
			c.fail("service", "start on boot", "boot = 1 but no enabled maio service",
				"run: sudo maio boot 1  (or: maio boot 1 --user)")
		}
	} else if userSt.Active || sysSt.Active {
		c.warn("service", "start on boot", "boot = 0 but a maio service is currently running",
			"the service will not start automatically after reboot")
	}
	if conf.RunAs != "" {
		if _, err := user.Lookup(conf.RunAs); err != nil {
			c.fail("service", "service user", fmt.Sprintf("run_as = %q does not exist", conf.RunAs),
				"create it with: sudo adduser --system --group --no-create-home "+conf.RunAs)
		} else {
			c.ok("service", "service user", conf.RunAs)
		}
	}
	activeMode := systemd.System
	if !sysSt.Exists && userSt.Exists {
		activeMode = systemd.User
	}
	if data, err := os.ReadFile(systemd.UnitPath(activeMode)); err == nil {
		unit := string(data)
		if !strings.Contains(unit, "Type=notify") || !strings.Contains(unit, "NoNewPrivileges=yes") {
			c.warn("service", "unit hardening", "the installed unit is from V2",
				"regenerate it with: sudo maio boot 1")
		} else {
			c.ok("service", "unit hardening", "notify + watchdog + sandbox present")
		}
	}

	// Index
	idx, idxErr := index.Load()
	if idxErr != nil {
		c.warn("index", "model index", idxErr.Error(), errs.HintOf(idxErr))
	} else if idx.Entries() == 0 {
		c.warn("index", "model index", "no entries; installs need --url",
			"add lines to "+index.PathCandidates()[0]+", or pass --url with a direct link")
	} else {
		c.ok("index", "model index", fmt.Sprintf("%d entries (%s)", idx.Entries(), idx.Path()))
	}

	// Models
	entries, warns := registry.Scan(conf.ModelsRoot)
	for _, w := range warns {
		c.warn("models", "registry", w.Error())
	}
	if len(entries) == 0 {
		c.warn("models", "installed models", "none",
			"install one with: sudo maio install <name> --url <url>")
	}
	for _, e := range entries {
		name := e.Model.Name
		if e.Err != nil {
			c.fail("models", name, e.Err.Error(), errs.HintOf(e.Err))
			continue
		}
		if e.Missing {
			c.fail("models", name, "model file is missing", fmt.Sprintf("reinstall it: sudo maio install %s", name))
			continue
		}
		info, err := gguf.Read(e.Model.FilePath())
		if err != nil {
			c.fail("models", name, err.Error(), errs.HintOf(err))
			continue
		}
		detail := fmt.Sprintf("%s, ctx %d, %s", humanSize(e.Size), info.ContextLength, info.Quant)
		if info.Vision {
			detail += ", vision"
			if mm := findMMProj(e.Model.Dir, e.Model.FilePath()); mm != "" {
				detail += " (" + filepath.Base(mm) + ")"
			} else {
				c.warn("models", name+" vision", "vision architecture but no mmproj*.gguf found")
			}
		}
		budget := vram.EstimateWith(info, e.Size+mmprojSize(e.Model.Dir, e.Model.FilePath()), vram.Options{
			Context: conf.CtxSize, Parallel: conf.Parallel, KVType: conf.KVType,
		}, hw)
		if hw.HasGPU() {
			detail += "; " + budget.Summary()
		}
		if hw.HasGPU() && !budget.Fits() {
			one := vram.EstimateWith(info, e.Size+mmprojSize(e.Model.Dir, e.Model.FilePath()), vram.Options{
				Context: conf.CtxSize, Parallel: 1, KVType: conf.KVType,
			}, hw)
			if one.Fits() {
				c.warn("models", name, detail,
					"the runtime will reduce parallel slots to 1; set parallel = 1 in maio.conf to make this explicit")
			} else {
				c.fail("models", name, detail, budget.Suggestion("ctx_size"))
			}
		} else {
			c.ok("models", name, detail)
		}
		if !budget.KVKnown && hw.HasGPU() {
			c.warn("models", name+" budget", "GGUF metadata is incomplete; the VRAM estimate is approximate")
		}
	}

	// Live service (only when a unit is active or the port answers)
	if stats, err := fetchStats(conf, 2*time.Second); err == nil {
		c.ok("service", "health", fmt.Sprintf("reachable, uptime %s", formatUptime(stats.Metrics.UptimeSeconds)))
		rss := stats.Metrics.RSSBytes
		if rss > 200<<20 {
			c.warn("service", "memory", fmt.Sprintf("control plane RSS is %s (target < 200 MiB)", humanBytes(rss)),
				"lower memory_limit_mb, or inspect: maio stats")
		} else {
			c.ok("service", "memory", fmt.Sprintf("control plane RSS %s (target < 200 MiB)", humanBytes(rss)))
		}
		if stats.Auth.Required {
			c.ok("service", "api auth", "requests require a key")
		} else if !stats.Auth.Loopback {
			c.warn("service", "api auth", "the API is reachable without a key")
		}
	} else if sysSt.Active || userSt.Active {
		var ae *apiError
		hint := "inspect: journalctl -u maio -n 50 (or --user)"
		detail := err.Error()
		if errors.As(err, &ae) && ae.Status == 404 {
			detail = "the running service does not expose /api/stats (it may be an older binary)"
			hint = "install/restart V3: sudo maio boot 1 (or: maio boot 1 --user), then: maio service restart"
		} else if errors.As(err, &ae) && ae.Status == 401 {
			hint = "read the key with: maio token --show"
		}
		c.fail("service", "health", detail, hint)
	}

	// Logs
	logPath := logging.Path()
	if logPath == "" {
		c.warn("logs", "audit log", "not available; the log directory is not writable")
	} else if st, err := os.Stat(logPath); err == nil {
		c.ok("logs", "audit log", fmt.Sprintf("%s (%s)", logPath, humanSize(st.Size())))
	} else {
		c.warn("logs", "audit log", fmt.Sprintf("%s not created yet", logPath))
	}

	ok, warn, fail := c.counts()
	if asJSON {
		report := doctorReport{
			Version: Version,
			Checks:  c.checks,
			Summary: map[string]int{"ok": ok, "warning": warn, "failure": fail},
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
	} else {
		printDoctor(c.checks, ok, warn, fail)
	}
	logging.Info("doctor", "ok", ok, "warnings", warn, "failures", fail)
	if fail > 0 {
		return 9
	}
	return 0
}

func printDoctor(checks []doctorCheck, ok, warn, fail int) {
	fmt.Printf("maio doctor %s\n\n", Version)
	last := ""
	for _, ch := range checks {
		if ch.Section != last {
			if last != "" {
				fmt.Println()
			}
			fmt.Printf("[%s]\n", ch.Section)
			last = ch.Section
		}
		label := map[string]string{"ok": "OK", "warn": "WARN", "fail": "FAIL"}[ch.Status]
		fmt.Printf("  %-4s %-28s %s\n", label, ch.Name, ch.Detail)
		if ch.Hint != "" && ch.Status != "ok" {
			fmt.Printf("       hint: %s\n", ch.Hint)
		}
	}
	fmt.Printf("\nsummary: %d ok, %d warning(s), %d failure(s)\n", ok, warn, fail)
	if fail > 0 {
		fmt.Println("fix the failures above, then run maio doctor again")
	}
}

func osPrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "unknown"
}

func commandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func writableDir(p string) bool {
	if err := os.MkdirAll(p, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(p, ".maio-doctor-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
