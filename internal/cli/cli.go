// Package cli implements the maio command line entry point and all subcommands.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio--cluster/internal/config"
	"github.com/ShieldDog-ACE/maio--cluster/internal/download"
	"github.com/ShieldDog-ACE/maio--cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio--cluster/internal/index"
	"github.com/ShieldDog-ACE/maio--cluster/internal/naming"
	"github.com/ShieldDog-ACE/maio--cluster/internal/registry"
	"github.com/ShieldDog-ACE/maio--cluster/internal/server"
	"github.com/ShieldDog-ACE/maio--cluster/internal/systemd"
)

// Version can be overridden at build time with -ldflags.
var Version = "0.1.0-dev"

// Run is the entry point and returns the exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printHelp()
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		printHelp()
		return 0
	case "sudo":
		return runSudo(args[1:])
	case "__as-root":
		return dispatch(args[1:])
	default:
		return dispatch(args)
	}
}

func runSudo(rest []string) int {
	if len(rest) == 0 {
		return usage("missing command after sudo")
	}
	if os.Geteuid() == 0 {
		return dispatch(rest)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(5, "cannot locate the maio executable: %v", err)
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fail(5, "sudo not found: run as root, or install sudo first")
	}
	cmd := exec.Command(sudo, append([]string{"--", exe, "__as-root"}, rest...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return fail(5, "failed to execute through sudo: %v", err)
	}
	return 0
}

func dispatch(args []string) int {
	if len(args) == 0 {
		printHelp()
		return 2
	}
	conf, err := config.LoadGlobal()
	if err != nil {
		return fail(2, "%v", err)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "install":
		return cmdInstall(conf, rest)
	case "list", "ls":
		return cmdList(conf)
	case "show":
		return cmdShow(conf, rest)
	case "rm", "uninstall":
		return cmdRm(conf, rest)
	case "run":
		return cmdRun(conf, rest)
	case "serve":
		return cmdServe(conf, rest)
	case "ps":
		return cmdPs(conf)
	case "stop":
		return cmdStop(conf, rest)
	case "boot":
		return cmdBoot(conf, rest)
	case "version", "--version":
		fmt.Printf("maio %s\n  go: %s\n  config: %s\n", Version, runtime.Version(), config.ConfPath())
		return 0
	default:
		return usage("unknown command %q (see maio --help)", cmd)
	}
}

func cmdInstall(conf *config.Global, args []string) int {
	var rawURL, root string
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: maio sudo install <name> [download-root] [--url direct-link-or-local-path]")
			return 0
		case "--url":
			if i+1 >= len(args) {
				return usage("--url requires a value")
			}
			i++
			rawURL = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return usage("unknown argument %s", args[i])
			}
			pos = append(pos, args[i])
		}
	}
	if len(pos) == 0 {
		return usage("missing model name, e.g. maio sudo install deepseek-v4.1 /data/models")
	}
	if len(pos) > 2 {
		return usage("too many arguments")
	}
	name, err := naming.Normalize(pos[0])
	if err != nil {
		return fail(2, "%v", err)
	}
	root = conf.ModelsRoot
	if len(pos) == 2 {
		root = pos[1]
	}
	if root == "" || !filepath.IsAbs(root) {
		return fail(2, "download root must be an absolute path: %q", root)
	}
	if err := ensureWritableRoot(root); err != nil {
		return fail(5, "%v", err)
	}
	if srcPath, ok := localSourcePath(rawURL); ok {
		return installLocal(name, root, srcPath)
	}
	if rawURL == "" {
		idx, err := index.Load()
		if err != nil {
			return fail(2, "%v", err)
		}
		u, ok := idx.URL(name)
		if !ok {
			return fail(3, "model %s is not in the index: pass --url, or add a line to index.toml", name)
		}
		rawURL = u
	}
	filename := fileNameFromURL(rawURL)
	if filename == "" {
		filename = name + ".gguf"
	}
	dir := config.ModelDir(root, name)
	if err := prepareInstallDir(dir); err != nil {
		return fail(4, "%v", err)
	}
	dest := filepath.Join(dir, filename)
	sha, _ := fetchSidecarSHA(rawURL)
	fmt.Printf("installing %s\n  source %s\n  target %s\n", name, rawURL, dest)
	if err := download.Fetch(context.Background(), download.Options{
		URL:          rawURL,
		DestPath:     dest,
		ExpectSHA256: sha,
		Log:          os.Stdout,
	}); err != nil {
		return fail(6, "%v", err)
	}
	if err := config.SaveModel(dir, name, "gguf", filename); err != nil {
		return fail(5, "failed to write maio.toml: %v", err)
	}
	fmt.Printf("done: %s\nnext: maio run %s\n", config.ModelConfigPath(dir), name)
	return 0
}

// localSourcePath reports whether --url is a local path (absolute, ./, ../ or file://).
func localSourcePath(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	p := raw
	local := false
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
		local = true
	}
	if !local && !(strings.HasPrefix(p, "/") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")) {
		return "", false
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	return abs, true
}

// installLocal registers an existing local GGUF: in place if it is already inside the model directory, otherwise hardlink or copy.
func installLocal(name, root, src string) int {
	st, err := os.Stat(src)
	if err != nil {
		return fail(3, "local file is not readable: %v", err)
	}
	if !st.Mode().IsRegular() {
		return fail(3, "local source is not a regular file: %s", src)
	}
	dir := config.ModelDir(root, name)
	if _, err := os.Stat(config.ModelConfigPath(dir)); err == nil {
		return fail(4, "model %s is already registered (%s exists)", name, config.ModelConfigPath(dir))
	}
	rel, relErr := filepath.Rel(dir, src)
	inPlace := relErr == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..")

	if inPlace {
		if err := download.VerifyMagic(src); err != nil {
			return fail(7, "verification failed: %v", err)
		}
		if err := config.SaveModel(dir, name, "gguf", filepath.ToSlash(rel)); err != nil {
			return fail(5, "failed to write maio.toml: %v", err)
		}
		fmt.Printf("registered %s in place (no copy)\n  config %s\n", name, config.ModelConfigPath(dir))
		return 0
	}

	if err := prepareInstallDir(dir); err != nil {
		return fail(4, "%v", err)
	}
	dest := filepath.Join(dir, filepath.Base(src))
	fmt.Printf("registering %s\n  source %s\n  target %s\n", name, src, dest)
	if err := download.CopyLocal(src, dest, os.Stdout); err != nil {
		return fail(5, "%v", err)
	}
	if err := download.VerifyMagic(dest); err != nil {
		return fail(7, "verification failed: %v", err)
	}
	if err := config.SaveModel(dir, name, "gguf", filepath.Base(src)); err != nil {
		return fail(5, "failed to write maio.toml: %v", err)
	}
	fmt.Printf("done: %s\n", config.ModelConfigPath(dir))
	return 0
}

func cmdList(conf *config.Global) int {
	entries, warns := registry.Scan(conf.ModelsRoot)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "warning: %v\n", w)
	}
	if len(entries) == 0 {
		fmt.Printf("(no models yet; download root is %s)\n", conf.ModelsRoot)
		return 0
	}
	fmt.Printf("%-24s %-8s %10s\n", "NAME", "FORMAT", "SIZE")
	for _, e := range entries {
		fmt.Printf("%-24s %-8s %10s\n", e.Model.Name, e.Model.Format, humanSize(e.Size))
	}
	return 0
}

func cmdShow(conf *config.Global, args []string) int {
	if len(args) != 1 {
		return usage("usage: maio show <name>")
	}
	md, err := registry.Find(conf.ModelsRoot, args[0])
	if err != nil {
		return fail(3, "%v", err)
	}
	fp := md.FilePath()
	st, statErr := os.Stat(fp)
	fmt.Printf("name   : %s\n", md.Name)
	fmt.Printf("format : %s\n", md.Format)
	fmt.Printf("path   : %s\n", fp)
	fmt.Printf("config : %s\n", config.ModelConfigPath(md.Dir))
	if statErr != nil {
		fmt.Printf("file   : missing (%v)\n", statErr)
		return 7
	}
	fmt.Printf("size   : %s\n", humanSize(st.Size()))
	return 0
}

func cmdRm(conf *config.Global, args []string) int {
	yes := false
	var pos []string
	for _, a := range args {
		switch {
		case a == "-y" || a == "--yes":
			yes = true
		case a == "-h" || a == "--help":
			fmt.Println("usage: maio rm <name> [-y]")
			return 0
		case strings.HasPrefix(a, "-"):
			return usage("unknown argument %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usage("usage: maio rm <name> [-y]")
	}
	md, err := registry.Find(conf.ModelsRoot, pos[0])
	if err != nil {
		return fail(3, "%v", err)
	}
	info, err := os.Stat(md.Dir)
	if err != nil || !info.IsDir() {
		return fail(3, "invalid model directory: %s", md.Dir)
	}
	if !yes {
		fmt.Printf("delete %s and its model files? [y/N] ", md.Name)
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("cancelled")
			return 0
		}
	}
	if err := os.RemoveAll(md.Dir); err != nil {
		return fail(5, "%v", err)
	}
	fmt.Printf("deleted %s\n", md.Dir)
	return 0
}

func cmdRun(conf *config.Global, args []string) int {
	var pos []string
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Println("usage: maio run <name> [prompt]")
			return 0
		case strings.HasPrefix(a, "-"):
			return usage("unknown argument %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return usage("usage: maio run <name> [prompt]")
	}
	md, err := registry.Find(conf.ModelsRoot, pos[0])
	if err != nil {
		return fail(3, "%v", err)
	}
	eng := engine.New(conf)
	inst, err := eng.Ensure(context.Background(), md)
	if err != nil {
		return fail(8, "%v", err)
	}
	defer func() {
		if err := eng.Stop(md.Name); err != nil {
			fmt.Fprintf(os.Stderr, "note: failed to stop engine: %v\n", err)
		}
	}()

	if prompt := strings.Join(pos[1:], " "); prompt != "" {
		_, code := chatStream(inst.Port, md.Name, []chatMessage{{Role: "user", Content: prompt}})
		return code
	}
	fmt.Printf("chatting with %s (type exit to quit)\n", md.Name)
	var history []chatMessage
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(">>> ")
		if !sc.Scan() {
			fmt.Println()
			return 0
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return 0
		}
		history = append(history, chatMessage{Role: "user", Content: line})
		reply, code := chatStream(inst.Port, md.Name, history)
		if code != 0 {
			return code
		}
		history = append(history, chatMessage{Role: "assistant", Content: reply})
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func chatStream(port int, model string, msgs []chatMessage) (string, int) {
	payload, err := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	if err != nil {
		return "", fail(8, "failed to build request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), bytes.NewReader(payload))
	if err != nil {
		return "", fail(8, "%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fail(8, "failed to reach engine: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fail(8, "engine returned HTTP %d", resp.StatusCode)
	}
	var reply strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			fmt.Print(c.Delta.Content)
			reply.WriteString(c.Delta.Content)
		}
	}
	fmt.Println()
	if err := sc.Err(); err != nil {
		return reply.String(), fail(8, "failed to read stream: %v", err)
	}
	return reply.String(), 0
}

func cmdServe(conf *config.Global, args []string) int {
	host, port := conf.Host, conf.Port
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: maio serve [--host address] [--port port]")
			return 0
		case "--host":
			if i+1 >= len(args) {
				return usage("--host requires a value")
			}
			i++
			host = args[i]
		case "--port":
			if i+1 >= len(args) {
				return usage("--port requires a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil || p < 1 || p > 65535 {
				return usage("invalid port: %s", args[i])
			}
			port = p
		default:
			return usage("unknown argument %s", args[i])
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := server.New(conf, engine.New(conf), Version)
	fmt.Printf("maio service started at http://%s\n", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		return fail(4, "failed to start service: %v", err)
	}
	return 0
}

func cmdPs(conf *config.Global) int {
	eng := engine.New(conf)
	insts, err := eng.List()
	if err != nil {
		return fail(8, "%v", err)
	}
	if len(insts) == 0 {
		fmt.Println("(no models loaded)")
		return 0
	}
	fmt.Printf("%-24s %6s %8s %-8s %s\n", "NAME", "PORT", "PID", "STATUS", "STARTED")
	for _, in := range insts {
		status := "ok"
		if eng.Health(in.Port) != nil {
			status = "dead"
		}
		fmt.Printf("%-24s %6d %8d %-8s %s\n", in.Name, in.Port, in.PID, status, in.Started)
	}
	return 0
}

func cmdStop(conf *config.Global, args []string) int {
	if len(args) != 1 {
		return usage("usage: maio stop <name>")
	}
	name, err := naming.Normalize(args[0])
	if err != nil {
		return fail(2, "%v", err)
	}
	if err := engine.New(conf).Stop(name); err != nil {
		return fail(8, "%v", err)
	}
	fmt.Printf("stopped %s\n", name)
	return 0
}

func cmdBoot(conf *config.Global, args []string) int {
	dry := false
	value := ""
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Println("usage: maio sudo boot 1|0 [--dry-run]")
			return 0
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			return usage("unknown argument %s", a)
		default:
			if value != "" {
				return usage("too many arguments")
			}
			value = a
		}
	}
	if value != "1" && value != "0" {
		return usage("usage: maio sudo boot 1|0 (1=start on boot, 0=manual start)")
	}
	conf.Boot = int(value[0] - '0')
	if dry {
		action := "enable --now"
		if value == "0" {
			action = "disable --now"
		}
		fmt.Printf("will set boot = %s\n  unit: /etc/systemd/system/maio.service\n  run: systemctl %s maio\n", value, action)
		return 0
	}
	if err := conf.Save(); err != nil {
		return fail(5, "failed to save global config (root required): %v", err)
	}
	if value == "1" {
		if err := systemd.Enable(installedExe(), config.ConfPath()); err != nil {
			return fail(5, "%v", err)
		}
		fmt.Println("start on boot enabled")
		return 0
	}
	if err := systemd.Disable(); err != nil {
		return fail(5, "%v", err)
	}
	fmt.Println("start on boot disabled")
	return 0
}

func ensureWritableRoot(root string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	systemLike := strings.HasPrefix(root, "/var/") ||
		strings.HasPrefix(root, "/etc/") ||
		strings.HasPrefix(root, "/usr/") ||
		strings.HasPrefix(root, "/opt/")
	if systemLike {
		return fmt.Errorf("installing to %s requires root, use: maio sudo install ...", root)
	}
	fmt.Fprintf(os.Stderr, "note: target %s is not a system path; writing as the current user\n", root)
	return nil
}

func prepareInstallDir(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".part") || e.Name() == "maio.toml" {
			continue
		}
		return fmt.Errorf("directory already exists and is not empty: %s (rename, or pass another download root)", dir)
	}
	return nil
}

func fileNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return base
}

func fetchSidecarSHA(rawURL string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL + ".sha256")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum file")
	}
	sum := strings.ToLower(strings.TrimPrefix(fields[0], "sha256:"))
	if len(sum) != 64 {
		return "", fmt.Errorf("malformed checksum")
	}
	return sum, nil
}

func installedExe() string {
	if p, err := exec.LookPath("maio"); err == nil {
		return p
	}
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "maio"
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func usage(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "maio: "+format+"\n", a...)
	return 2
}

func fail(code int, format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "maio: "+format+"\n", a...)
	return code
}

func printHelp() {
	fmt.Print(`maio - a lightweight model management CLI built on llama.cpp

usage: maio [sudo] <command> [args]

install
  sudo install maio-c                 install maio itself
  maio sudo install <name> [root]     install a model into <root>/<name>/
  maio sudo install <name> --url URL  install from a direct link
  maio sudo install <name> --url /abs/x.gguf  register a local model in place

models
  maio list                           list installed models
  maio show <name>                    show config and file path
  maio rm <name>                      delete a model
  maio run <name> [prompt]            chat / one-shot inference

service
  maio serve                          start the LAN service
  maio ps                             list loaded models
  maio stop <name>                    unload a model and free VRAM

system
  maio sudo boot 1|0                  start on boot: 1=on, 0=off
  maio version                        print version

note: model names are lowercase; spaces become - (DeepSeek v4.1 -> deepseek-v4.1)
`)
}
