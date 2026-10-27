package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

func cmdVersion(args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio version [--json]")
			return 0
		case "--json":
			asJSON = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	conf, err := config.LoadGlobal()
	confErr := ""
	if err != nil {
		confErr = err.Error()
		conf = config.DefaultGlobal()
	}
	rec := map[string]any{
		"version":         Version,
		"api":             "ollama+openai-compatible",
		"go":              runtime.Version(),
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"config":          config.ConfPath(),
		"models_root":     conf.ModelsRoot,
		"runtime_dir":     conf.RuntimeDir,
		"log":             conf.AuditLogPath(),
		"key_file":        conf.AuthKeyFile(),
		"listen":          fmt.Sprintf("%s:%d", conf.Host, conf.Port),
		"auth":            conf.Auth,
		"memory_limit_mb": conf.MemoryLimitMB,
		"max_concurrency": conf.MaxConcurrency,
		"max_queue":       conf.MaxQueue,
		"parallel":        conf.Parallel,
		"kv_type":         conf.KVType,
	}
	if confErr != "" {
		rec["config_error"] = confErr
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rec)
		return 0
	}
	fmt.Printf("maio %s\n", Version)
	fmt.Printf("  go          : %s\n", runtime.Version())
	fmt.Printf("  platform    : %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  config      : %s\n", config.ConfPath())
	fmt.Printf("  models_root : %s\n", conf.ModelsRoot)
	fmt.Printf("  runtime_dir : %s\n", conf.RuntimeDir)
	fmt.Printf("  audit log   : %s\n", conf.AuditLogPath())
	fmt.Printf("  api         : ollama + openai-compatible\n")
	fmt.Printf("  listen      : %s:%d (auth %s)\n", conf.Host, conf.Port, conf.Auth)
	fmt.Printf("  memory      : limit %d MiB, concurrency %d, queue %d\n",
		conf.MemoryLimitMB, conf.MaxConcurrency, conf.MaxQueue)
	if confErr != "" {
		fmt.Fprintf(os.Stderr, "maio: warning: %s\n", confErr)
	}
	return 0
}

func cmdLogs(conf *config.Global, args []string) int {
	tail := 50
	asJSON := false
	showPath := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: maio logs [--tail N] [--json] [--path]")
			return 0
		case "--json":
			asJSON = true
		case "--path":
			showPath = true
		case "--tail":
			if i+1 >= len(args) {
				return usagef("--tail requires a number (0 = all lines)")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return usagef("invalid --tail value %q", args[i])
			}
			tail = n
		default:
			return usagef("unknown argument %s", args[i])
		}
	}
	path := logging.Path()
	if path == "" {
		path = conf.AuditLogPath()
	}
	if showPath {
		fmt.Println(path)
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("(no audit log yet: %s)\n", path)
			return 0
		}
		if os.IsPermission(err) {
			return fail(errs.Wrap(errs.Permission, err, "cannot read the audit log %s", path).
				WithHint("run with sudo to read the system log, or use MAIO_LOG"))
		}
		return fail(errs.Wrap(errs.IO, err, "cannot read the audit log %s", path))
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		fmt.Printf("(audit log is empty: %s)\n", path)
		return 0
	}
	for _, line := range lines {
		if asJSON {
			fmt.Println(line)
			continue
		}
		fmt.Println(formatLogLine(line))
	}
	return 0
}

func formatLogLine(line string) string {
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return line
	}
	ts, _ := entry["time"].(string)
	level, _ := entry["level"].(string)
	action, _ := entry["action"].(string)
	keys := make([]string, 0, len(entry))
	for k := range entry {
		switch k {
		case "time", "level", "action":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-5s  %s", ts, strings.ToUpper(level), action)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s=%s", k, logValue(entry[k]))
	}
	return b.String()
}

func logValue(v any) string {
	switch x := v.(type) {
	case string:
		if strings.ContainsAny(x, " \t\n=\"") {
			return strconv.Quote(x)
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
