package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/auth"
	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

func cmdToken(conf *config.Global, args []string) int {
	show := false
	asJSON := false
	restart := false
	action := "show"
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio token [show] [--show] [--json]")
			fmt.Println("       maio token rotate [--restart]")
			fmt.Println("       maio token set <key>")
			fmt.Println("       maio token clear")
			return 0
		case "--show":
			show = true
		case "--json":
			asJSON = true
		case "--restart":
			restart = true
		default:
			if strings.HasPrefix(a, "-") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) > 0 {
		action = pos[0]
		pos = pos[1:]
	}
	keyFile := conf.AuthKeyFile()
	key, source, lerr := auth.Lookup(auth.Options{
		Mode: conf.Auth, Key: conf.APIKey, KeyFile: keyFile,
		Host: conf.Host, AllowInsecure: conf.AllowInsecure == 1,
	})
	if lerr != nil {
		return fail(lerr)
	}
	code := 0
	switch action {
	case "show":
		if len(pos) != 0 {
			return usagef("usage: maio token [show] [--show]")
		}
		if key == "" {
			if asJSON {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"configured": false, "key_file": keyFile, "auth": conf.Auth,
					"note": "no key is configured",
				})
				return 0
			}
			fmt.Printf("no API key is configured\n  key file: %s\n", keyFile)
			if auth.IsLoopbackHost(conf.Host) {
				fmt.Println("  auth is not required for a loopback listener")
			} else {
				fmt.Println("  the key is generated automatically on the next service start")
			}
			return 0
		}
		printed := auth.Mask(key)
		if show {
			printed = key
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"configured": true, "key": printed, "source": source,
				"key_file": keyFile, "required": conf.Auth != "off",
			})
			return 0
		}
		fmt.Printf("API key    : %s\n  source   : %s\n", printed, source)
		if source == "file" || source == "generated" {
			fmt.Printf("  key file : %s\n", keyFile)
		}
		if !show && source == "file" {
			fmt.Println("  show all : maio token --show")
		}
		return 0
	case "rotate":
		if len(pos) != 0 {
			return usagef("usage: maio token rotate [--restart]")
		}
		newKey, err := auth.GenerateAndSave(keyFile)
		if err != nil {
			return fail(err)
		}
		fixKeyOwnership(conf, keyFile)
		logging.Info("auth.rotate", "path", keyFile)
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"rotated": true, "key": newKey, "key_file": keyFile,
			})
		} else {
			fmt.Printf("rotated API key: %s\n  key file: %s\n", newKey, keyFile)
			if source == "env" || source == "config" {
				fmt.Fprintf(os.Stderr, "maio: warning: %s takes precedence over the key file; update or remove it\n",
					strings.ToUpper(source))
			}
			fmt.Println("restart the service to apply: maio service restart")
		}
		if restart {
			code = cmdRestart(conf, nil)
		}
		return code
	case "set":
		if len(pos) != 1 {
			return usagef("usage: maio token set <key>")
		}
		value := strings.TrimSpace(pos[0])
		if err := auth.ValidateKey(value); err != nil {
			return fail(err)
		}
		if err := auth.SaveKey(keyFile, value); err != nil {
			return fail(err)
		}
		fixKeyOwnership(conf, keyFile)
		logging.Info("auth.set", "path", keyFile)
		fmt.Printf("saved API key to %s\nrestart the service to apply: maio service restart\n", keyFile)
		if restart {
			code = cmdRestart(conf, nil)
		}
		return code
	case "clear":
		if len(pos) != 0 {
			return usagef("usage: maio token clear")
		}
		if err := os.Remove(keyFile); err != nil && !os.IsNotExist(err) {
			return fail(errs.Wrap(errs.Permission, err, "cannot remove %s", keyFile).
				WithHint("run with sudo for the system service"))
		}
		logging.Info("auth.clear", "path", keyFile)
		fmt.Printf("removed %s\n", keyFile)
		if conf.Auth != "off" && !auth.IsLoopbackHost(conf.Host) {
			fmt.Println("a new key will be generated on the next service start")
		}
		if restart {
			code = cmdRestart(conf, nil)
		}
		return code
	default:
		return usagef("unknown token action %q", action)
	}
}

// fixKeyOwnership hands a root-created key and its auth directory to the
// service user, otherwise a systemd service running as maio cannot read the
// 0600 key that "sudo maio token rotate" just wrote.
func fixKeyOwnership(conf *config.Global, keyFile string) {
	if os.Geteuid() != 0 || conf.RunAs == "" || strings.HasPrefix(keyFile, "/home/") {
		return
	}
	u, err := user.Lookup(conf.RunAs)
	if err != nil {
		return
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return
	}
	_ = os.Chown(keyFile, uid, gid)
	_ = os.Chmod(keyFile, 0o600)
	dir := filepath.Dir(keyFile)
	_ = os.Chown(dir, uid, gid)
	_ = os.Chmod(dir, 0o700)
}
