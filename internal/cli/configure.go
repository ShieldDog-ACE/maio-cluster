package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/systemd"
)

// configFields maps the flat maio.conf keys to Go fields. The reflection-based
// accessors keep "config get/set" in sync with the struct without a second
// hand-written parser.
var configFields = map[string]string{
	"boot": "Boot", "models_root": "ModelsRoot", "state_dir": "StateDir",
	"host": "Host", "port": "Port", "mirror": "Mirror",
	"llama_bin": "LlamaBin", "llama_lib": "LlamaLib", "ctx_size": "CtxSize",
	"auth": "Auth", "api_key": "APIKey", "allow_insecure": "AllowInsecure",
	"rate_limit": "RateLimit", "rate_burst": "RateBurst",
	"max_concurrency": "MaxConcurrency", "max_queue": "MaxQueue",
	"queue_timeout": "QueueTimeout", "request_timeout": "RequestTimeout",
	"max_body_mb": "MaxBodyMB", "idle_unload": "IdleUnload", "access_log": "AccessLog",
	"trust_proxy": "TrustProxy", "cors": "CORS", "parallel": "Parallel", "threads": "Threads",
	"kv_type": "KVType", "flash_attn": "FlashAttn", "mmap": "Mmap",
	"llama_log_verbosity": "LogVerbosity",
	"memory_limit_mb":     "MemoryLimitMB", "max_procs": "MaxProcs",
	"max_open_files": "MaxOpenFiles", "restore": "Restore", "run_as": "RunAs",
	"tls_cert": "TLSCert", "tls_key": "TLSKey", "tls_ca": "TLSCA",
}

func cmdConfig(conf *config.Global, args []string) int {
	if len(args) == 0 {
		return configShow(conf, false)
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Println("usage: maio config [show] [--json]")
		fmt.Println("       maio config path")
		fmt.Println("       maio config get <key>")
		fmt.Println("       maio config set <key> <value> [--user] [--restart]")
		fmt.Println("       maio config edit [--user]")
		fmt.Println("       maio config keys")
		return 0
	case "show":
		asJSON := false
		for _, a := range args[1:] {
			if a == "--json" {
				asJSON = true
			} else {
				return usagef("unknown argument %s", a)
			}
		}
		return configShow(conf, asJSON)
	case "path":
		if len(args) != 1 {
			return usagef("usage: maio config path")
		}
		fmt.Println(conf.Source)
		return 0
	case "keys":
		for _, k := range configKeyOrder() {
			fmt.Println(k)
		}
		return 0
	case "get":
		if len(args) != 2 {
			return usagef("usage: maio config get <key>")
		}
		v, err := configGet(conf, args[1])
		if err != nil {
			return fail(err)
		}
		fmt.Println(v)
		return 0
	case "set":
		return configSet(conf, args[1:])
	case "edit":
		return configEdit(conf, args[1:])
	default:
		return usagef("unknown config action %q", args[0])
	}
}

func configKeyOrder() []string {
	keys := make([]string, 0, len(configFields))
	for k := range configFields {
		keys = append(keys, k)
	}
	// stable, readable order by the struct declaration instead of map order
	order := []string{
		"boot", "models_root", "state_dir", "mirror",
		"host", "port", "auth", "api_key", "allow_insecure", "trust_proxy", "cors", "tls_cert", "tls_key", "tls_ca",
		"rate_limit", "rate_burst", "max_concurrency", "max_queue", "queue_timeout",
		"request_timeout", "max_body_mb", "idle_unload", "access_log",
		"llama_bin", "llama_lib", "ctx_size", "parallel", "threads", "kv_type", "flash_attn", "mmap",
		"llama_log_verbosity",
		"memory_limit_mb", "max_procs", "max_open_files", "restore", "run_as",
	}
	seen := make(map[string]bool, len(order))
	out := make([]string, 0, len(keys))
	for _, k := range order {
		if _, ok := configFields[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range keys {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func configShow(conf *config.Global, asJSON bool) int {
	if asJSON {
		rec := map[string]any{
			"source":      conf.Source,
			"runtime_dir": conf.RuntimeDir,
			"key_file":    conf.AuthKeyFile(),
			"lock_file":   conf.LockFile(),
		}
		values := make(map[string]any, len(configFields))
		for _, k := range configKeyOrder() {
			v, err := configGetValue(conf, k)
			if err != nil {
				continue
			}
			if k == "api_key" {
				if s, _ := v.(string); s != "" {
					v = "[redacted]"
				}
			}
			values[k] = v
		}
		rec["values"] = values
		_ = json.NewEncoder(os.Stdout).Encode(rec)
		return 0
	}
	fmt.Printf("# source: %s\n", conf.Source)
	fmt.Printf("# runtime_dir: %s\n", conf.RuntimeDir)
	fmt.Printf("# key_file: %s\n\n", conf.AuthKeyFile())
	fmt.Print(conf.Render())
	return 0
}

func configGet(conf *config.Global, key string) (string, error) {
	v, err := configGetValue(conf, key)
	if err != nil {
		return "", err
	}
	if key == "api_key" {
		s, _ := v.(string)
		if s == "" {
			return "", nil
		}
		return "[redacted; use: maio token --show]", nil
	}
	return fmt.Sprint(v), nil
}

func configGetValue(conf *config.Global, key string) (any, error) {
	field, ok := configFields[strings.TrimSpace(key)]
	if !ok {
		return nil, errs.Newf(errs.Usage, "unknown config key %q", key).
			WithHint("run \"maio config keys\" for the complete list")
	}
	v := reflect.ValueOf(conf).Elem().FieldByName(field)
	if !v.IsValid() {
		return nil, errs.Newf(errs.Internal, "config field %s is missing", field)
	}
	return v.Interface(), nil
}

func configSet(conf *config.Global, args []string) int {
	userMode := false
	restart := false
	var pos []string
	for _, a := range args {
		switch a {
		case "--user":
			userMode = true
		case "--restart":
			restart = true
		case "-h", "--help":
			fmt.Println("usage: maio config set <key> <value> [--user] [--restart]")
			return 0
		default:
			if strings.HasPrefix(a, "--") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return usagef("usage: maio config set <key> <value> [--user] [--restart]")
	}
	key := pos[0]
	value := strings.Join(pos[1:], " ")
	field, ok := configFields[key]
	if !ok {
		return usagef("unknown config key %q (run: maio config keys)", key)
	}
	fv := reflect.ValueOf(conf).Elem().FieldByName(field)
	switch fv.Kind() {
	case reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return usagef("%s must be an integer (got %q)", key, value)
		}
		fv.SetInt(int64(n))
	case reflect.String:
		fv.SetString(value)
	default:
		return fail(errs.Newf(errs.Internal, "unsupported config field type for %s", key))
	}
	path := conf.Source
	if path == "" {
		path = config.ConfPath()
	}
	if userMode {
		path = config.UserConfPath()
	}
	conf.Source = path
	if err := conf.SaveTo(path); err != nil {
		return fail(err)
	}
	fmt.Printf("updated %s: %s = %s\n", path, key, redactConfigValue(key, value))
	logging.Info("config.set", "path", path, "key", key, "user", userMode)
	if restart {
		return cmdRestart(conf, nil)
	}
	if st := serviceActive(conf); st {
		fmt.Println("restart the service to apply: maio service restart")
	}
	return 0
}

func redactConfigValue(key, value string) string {
	if key == "api_key" {
		return "[redacted]"
	}
	return value
}

func configEdit(conf *config.Global, args []string) int {
	userMode := false
	for _, a := range args {
		switch a {
		case "--user":
			userMode = true
		case "-h", "--help":
			fmt.Println("usage: maio config edit [--user]")
			return 0
		default:
			return usagef("unknown argument %s", a)
		}
	}
	path := conf.Source
	if path == "" {
		path = config.ConfPath()
	}
	if userMode {
		path = config.UserConfPath()
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "nano"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fail(errs.Wrap(errs.Usage, err, "editor %s failed", editor))
	}
	old := os.Getenv("MAIO_CONF")
	_ = os.Setenv("MAIO_CONF", path)
	_, err := config.LoadGlobal()
	if old == "" {
		_ = os.Unsetenv("MAIO_CONF")
	} else {
		_ = os.Setenv("MAIO_CONF", old)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Printf("config is valid: %s\n", path)
	fmt.Println("restart the service to apply: maio service restart")
	return 0
}

func serviceActive(conf *config.Global) bool {
	mode, installed := serviceMode(conf, false)
	if !installed {
		return false
	}
	return systemd.Query(mode).Active
}
