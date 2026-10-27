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
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/download"
	"github.com/ShieldDog-ACE/maio-cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/index"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
	"github.com/ShieldDog-ACE/maio-cluster/internal/server"
	"github.com/ShieldDog-ACE/maio-cluster/internal/systemd"
	"github.com/ShieldDog-ACE/maio-cluster/internal/vram"
)

// Version can be overridden at build time with -ldflags.
var Version = "2.0.0-dev"

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
	case "version", "--version", "-v":
		return cmdVersion(args[1:])
	case "sudo":
		err := errs.New(errs.Usage, `"maio sudo" is no longer supported`).
			WithHint("use \"sudo maio %s\" instead", strings.Join(args[1:], " "))
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	conf, err := config.LoadGlobal()
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	logging.Init(conf.RuntimeDir)
	defer logging.Close()
	start := time.Now()
	logging.Info("cli.start", "command", args[0], "args", sanitizeArgs(args[1:]))
	code := dispatch(conf, args)
	logging.Info("cli.end", "command", args[0], "exit", code, "duration_ms", time.Since(start).Milliseconds())
	return code
}

func dispatch(conf *config.Global, args []string) int {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "install":
		return cmdInstall(conf, rest)
	case "list", "ls":
		return cmdList(conf, rest)
	case "show":
		return cmdShow(conf, rest)
	case "rm", "uninstall":
		return cmdRm(conf, rest)
	case "run":
		return cmdRun(conf, rest)
	case "serve":
		return cmdServe(conf, rest)
	case "ps":
		return cmdPs(conf, rest)
	case "stop":
		return cmdStop(conf, rest)
	case "boot":
		return cmdBoot(conf, rest)
	case "doctor":
		return cmdDoctor(conf, rest)
	case "logs", "log":
		return cmdLogs(conf, rest)
	case "version", "--version":
		return cmdVersion(rest)
	default:
		err := errs.Newf(errs.Usage, "unknown command %q", cmd).
			WithHint("run \"maio --help\" to see all commands")
		return fail(err)
	}
}

func fail(err error) int {
	if err == nil {
		return 0
	}
	errs.Print(os.Stderr, err)
	logging.Error("command.error", err)
	return errs.ExitCode(err)
}

func usagef(format string, args ...any) int {
	return fail(errs.Newf(errs.Usage, format, args...).WithHint("run \"maio --help\" for usage"))
}

func cmdInstall(conf *config.Global, args []string) int {
	var rawURL, sha string
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: sudo maio install <name> [download-root] [--url URL|PATH] [--sha256 HEX]")
			fmt.Println("       maio install <name> ~/models --url ./model.gguf   (user-owned root, no sudo)")
			return 0
		case "--url":
			if i+1 >= len(args) {
				return usagef("--url requires a value")
			}
			i++
			rawURL = args[i]
		case "--sha256":
			if i+1 >= len(args) {
				return usagef("--sha256 requires a 64-character hex value")
			}
			i++
			sha = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(args[i]), "sha256:"))
		default:
			if strings.HasPrefix(args[i], "-") {
				return usagef("unknown argument %s", args[i])
			}
			pos = append(pos, args[i])
		}
	}
	if len(pos) == 0 {
		return usagef("missing model name, e.g. sudo maio install deepseek-v4.1 /data/models")
	}
	if len(pos) > 2 {
		return usagef("too many arguments")
	}
	if sha != "" {
		if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
			return fail(errs.New(errs.Usage, "--sha256 must be 64 hexadecimal characters").
				WithHint("copy the SHA-256 published by the model source, or omit --sha256"))
		}
	}
	name, err := naming.Normalize(pos[0])
	if err != nil {
		return fail(err)
	}
	root := conf.ModelsRoot
	if len(pos) == 2 {
		root = pos[1]
	}
	if root == "" || !filepath.IsAbs(root) {
		return fail(errs.Newf(errs.Usage, "download root must be an absolute path: %q", root).
			WithHint("example: sudo maio install %s /data/models", name))
	}
	if err := ensureWritableRoot(root, name); err != nil {
		return fail(err)
	}
	dir := config.ModelDir(root, name)
	if _, err := os.Stat(config.ModelConfigPath(dir)); err == nil {
		return fail(errs.Newf(errs.Conflict, "model %s is already installed in %s", name, dir).
			WithHint("use maio show %s, or remove it first with maio rm %s", name, name))
	}
	if rawURL != "" {
		kind, localPath, remoteURL, err := classifySource(rawURL)
		if err != nil {
			return fail(err)
		}
		if kind == "local" {
			return installLocal(conf, name, root, localPath, sha)
		}
		return installRemote(conf, name, root, remoteURL, sha)
	}
	idx, err := index.Load()
	if err != nil {
		return fail(err)
	}
	u, ok := idx.URL(name)
	if !ok {
		return fail(errs.Newf(errs.NotFound, "model %s is not in the index", name).
			WithHint("pass --url with a direct link or a local GGUF path, or add the model to %s", index.PathCandidates()[0]))
	}
	return installRemote(conf, name, root, u, sha)
}

// classifySource decides whether --url is a remote URL or a local model file.
func classifySource(raw string) (kind, localPath, remoteURL string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errs.New(errs.Usage, "--url is empty")
	}
	if strings.HasPrefix(raw, "file://") {
		u, perr := url.Parse(raw)
		if perr != nil || (u.Host != "" && u.Host != "localhost") {
			return "", "", "", errs.Newf(errs.Usage, "unsupported file URL: %s", raw).
				WithHint("use file:///absolute/path.gguf or a plain absolute path")
		}
		return "local", expandHome(u.Path), "", nil
	}
	if strings.Contains(raw, "://") {
		u, perr := url.Parse(raw)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", "", errs.Newf(errs.Usage, "unsupported URL: %s", raw).
				WithHint("use http://, https://, file:// or a local path")
		}
		return "remote", "", raw, nil
	}
	if strings.HasPrefix(raw, "~/") {
		return "local", expandHome(raw), "", nil
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		abs, aerr := filepath.Abs(raw)
		if aerr != nil {
			return "", "", "", errs.Wrap(errs.Usage, aerr, "invalid path %s", raw)
		}
		return "local", abs, "", nil
	}
	if st, serr := os.Stat(raw); serr == nil && !st.IsDir() {
		abs, _ := filepath.Abs(raw)
		return "local", abs, "", nil
	}
	if strings.HasSuffix(strings.ToLower(raw), ".gguf") || strings.ContainsAny(raw, `/\`) {
		return "local", raw, "", nil
	}
	return "", "", "", errs.Newf(errs.Usage, "--url value %q is neither an http(s) URL nor a local path", raw).
		WithHint("example: --url https://example.com/model.gguf  or  --url /data/models/model.gguf")
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func installRemote(conf *config.Global, name, root, rawURL, sha string) int {
	filename := download.FileNameFromURL(rawURL)
	if filename == "" || !strings.HasSuffix(strings.ToLower(filename), ".gguf") {
		filename = name + ".gguf"
	}
	dir := config.ModelDir(root, name)
	if err := prepareInstallDir(dir); err != nil {
		return fail(err)
	}
	dest := filepath.Join(dir, filename)
	if sha == "" {
		sidecar, serr := fetchSidecarSHA(rawURL)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "maio: warning: no checksum sidecar found for %s; SHA-256 verification will be skipped\n", rawURL)
			logging.Warn("install.sha256_missing", "url", rawURL, "error", serr.Error())
		} else {
			sha = sidecar
		}
	}
	if remote := download.RemoteSize(context.Background(), nil, rawURL); remote > 0 {
		if err := download.EnsureSpace(dir, uint64(remote)); err != nil {
			return fail(err)
		}
	}
	fmt.Printf("installing %s\n  source %s\n  target %s\n", name, rawURL, dest)
	logging.Info("install.start", "model", name, "source", rawURL, "dest", dest, "root", root, "sha256", sha)
	start := time.Now()
	ctx, stop := signalContext()
	defer stop()
	if err := download.Fetch(ctx, download.Options{
		URL:          rawURL,
		DestPath:     dest,
		ExpectSHA256: sha,
		Log:          os.Stdout,
	}); err != nil {
		logging.Error("install.failed", err, "model", name, "source", rawURL, "dest", dest)
		return fail(err)
	}
	if err := validateGGUF(dest); err != nil {
		logging.Error("install.invalid_gguf", err, "model", name, "dest", dest)
		_ = os.Remove(dest)
		return fail(err)
	}
	if err := config.SaveModel(dir, name, "gguf", filename); err != nil {
		_ = os.Remove(dest)
		return fail(errs.Wrap(errs.IO, err, "the model was downloaded but maio.toml could not be written").
			WithHint("fix the directory permissions and rerun; the incomplete file was removed"))
	}
	st, _ := os.Stat(dest)
	var size int64
	if st != nil {
		size = st.Size()
	}
	fmt.Printf("done: %s\nnext: maio run %s\n", config.ModelConfigPath(dir), name)
	logging.Info("install.done", "model", name, "source", rawURL, "dest", dest,
		"bytes", size, "sha256", sha, "duration_ms", time.Since(start).Milliseconds())
	return 0
}

func installLocal(conf *config.Global, name, root, src, sha string) int {
	st, err := os.Stat(src)
	if err != nil {
		return fail(errs.Newf(errs.NotFound, "local model is not readable: %s", src).
			WithHint("check the path and file permissions"))
	}
	if !st.Mode().IsRegular() {
		return fail(errs.Newf(errs.Usage, "local source is not a regular file: %s", src))
	}
	dir := config.ModelDir(root, name)
	if _, err := os.Stat(config.ModelConfigPath(dir)); err == nil {
		return fail(errs.Newf(errs.Conflict, "model %s is already registered in %s", name, dir).
			WithHint("use maio show %s, or remove it first with maio rm %s", name, name))
	}
	rel, relErr := filepath.Rel(dir, src)
	inPlace := relErr == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..")
	logging.Info("install.local.start", "model", name, "source", src, "root", root, "in_place", inPlace)
	if inPlace {
		if err := validateLocal(src, sha); err != nil {
			return fail(err)
		}
		if err := config.SaveModel(dir, name, "gguf", filepath.ToSlash(rel)); err != nil {
			return fail(err)
		}
		fmt.Printf("registered %s in place (no copy)\n  config %s\n", name, config.ModelConfigPath(dir))
		logging.Info("install.local.done", "model", name, "source", src, "mode", "in-place", "bytes", st.Size())
		return 0
	}
	if err := prepareInstallDir(dir); err != nil {
		return fail(err)
	}
	dest := filepath.Join(dir, filepath.Base(src))
	fmt.Printf("registering %s\n  source %s\n  target %s\n", name, src, dest)
	mode, err := download.CopyLocal(src, dest, os.Stdout)
	if err != nil {
		logging.Error("install.local.failed", err, "model", name, "source", src)
		return fail(err)
	}
	if err := validateLocal(dest, sha); err != nil {
		_ = os.Remove(dest)
		return fail(err)
	}
	if err := config.SaveModel(dir, name, "gguf", filepath.Base(src)); err != nil {
		_ = os.Remove(dest)
		return fail(errs.Wrap(errs.IO, err, "the model was copied but maio.toml could not be written").
			WithHint("fix the directory permissions and rerun; the incomplete file was removed"))
	}
	fmt.Printf("done: %s\nnext: maio run %s\n", config.ModelConfigPath(dir), name)
	logging.Info("install.local.done", "model", name, "source", src, "dest", dest, "mode", mode, "bytes", st.Size())
	return 0
}

func validateLocal(path, sha string) error {
	if sha != "" {
		if err := download.Verify(path, sha); err != nil {
			return err
		}
	} else if err := download.VerifyMagic(path); err != nil {
		return err
	}
	return validateGGUF(path)
}

func validateGGUF(path string) error {
	if _, err := gguf.Read(path); err != nil {
		return err
	}
	return nil
}

func prepareInstallDir(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return errs.Wrap(errs.Permission, mkErr, "cannot create model directory %s", dir).
					WithHint("check permissions, or choose another download root")
			}
			return nil
		}
		return errs.Wrap(errs.Permission, err, "cannot read model directory %s", dir)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".part") || e.Name() == "maio.toml" {
			continue
		}
		return errs.Newf(errs.Conflict, "directory is not empty: %s", dir).
			WithHint("rename or remove it, or pass another download root")
	}
	return nil
}

func ensureWritableRoot(root, name string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	systemLike := false
	for _, prefix := range []string{"/var/", "/etc/", "/usr/", "/opt/", "/root/", "/srv/"} {
		if strings.HasPrefix(root, prefix) {
			systemLike = true
			break
		}
	}
	if systemLike {
		return errs.Newf(errs.Permission, "installing to %s requires root", root).
			WithHint("use: sudo maio install %s %s", name, root)
	}
	fmt.Fprintf(os.Stderr, "maio: note: %s is user-owned; writing as the current user\n", root)
	return nil
}

func fetchSidecarSHA(rawURL string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
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
	if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
		return "", fmt.Errorf("malformed checksum")
	}
	return sum, nil
}

func cmdList(conf *config.Global, args []string) int {
	asJSON := false
	wide := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio list [--json] [--wide]")
			return 0
		case "--json":
			asJSON = true
		case "--wide":
			wide = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	entries, warns := registry.Scan(conf.ModelsRoot)
	for _, w := range warns {
		if errs.IsCode(w, errs.Permission) || errs.IsCode(w, errs.IO) {
			return fail(w)
		}
		fmt.Fprintf(os.Stderr, "maio: warning: %v\n", w)
		logging.Warn("registry.warning", "error", w.Error())
	}
	type row struct {
		Name    string `json:"name"`
		Format  string `json:"format"`
		Size    int64  `json:"size"`
		Context uint32 `json:"context_length,omitempty"`
		Quant   string `json:"quantization,omitempty"`
		Vision  bool   `json:"vision"`
		Missing bool   `json:"missing"`
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		r := row{Name: e.Model.Name, Format: e.Model.Format, Size: e.Size, Missing: e.Missing}
		if wide && !e.Missing {
			if info, err := gguf.Read(e.Model.FilePath()); err == nil {
				r.Context = info.ContextLength
				r.Quant = info.Quant
				r.Vision = info.Vision
			} else {
				r.Quant = "?"
			}
		}
		rows = append(rows, r)
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Printf("(no models installed; models_root is %s)\n", conf.ModelsRoot)
		return 0
	}
	if wide {
		fmt.Printf("%-24s %-8s %10s %10s %-9s %-4s\n", "NAME", "FORMAT", "SIZE", "CONTEXT", "QUANT", "VIS")
		for _, r := range rows {
			status := "-"
			if r.Missing {
				status = "MISSING"
			}
			ctx := "-"
			if r.Context > 0 {
				ctx = strconv.FormatUint(uint64(r.Context), 10)
			}
			fmt.Printf("%-24s %-8s %10s %10s %-9s %-4s", r.Name, r.Format, humanSize(r.Size), ctx, r.Quant, boolYes(r.Vision))
			if status != "-" {
				fmt.Printf(" %s", status)
			}
			fmt.Println()
		}
		return 0
	}
	fmt.Printf("%-24s %-8s %10s\n", "NAME", "FORMAT", "SIZE")
	for _, r := range rows {
		size := humanSize(r.Size)
		if r.Missing {
			size = "MISSING"
		}
		fmt.Printf("%-24s %-8s %10s\n", r.Name, r.Format, size)
	}
	return 0
}

func cmdShow(conf *config.Global, args []string) int {
	asJSON := false
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio show <name> [--json]")
			return 0
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(a, "-") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usagef("usage: maio show <name>")
	}
	md, err := registry.Find(conf.ModelsRoot, pos[0])
	if err != nil {
		return fail(err)
	}
	fp := md.FilePath()
	st, statErr := os.Stat(fp)
	record := map[string]any{
		"name":   md.Name,
		"format": md.Format,
		"path":   fp,
		"config": config.ModelConfigPath(md.Dir),
	}
	if statErr != nil {
		record["missing"] = true
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(record)
		} else {
			fmt.Printf("name   : %s\nformat : %s\npath   : %s\nconfig : %s\nfile   : missing (%v)\n",
				md.Name, md.Format, fp, config.ModelConfigPath(md.Dir), statErr)
		}
		if os.IsPermission(statErr) {
			return fail(errs.Wrap(errs.Permission, statErr, "cannot read model file %s", fp).
				WithHint("check file permissions, or run with sudo"))
		}
		return fail(errs.Newf(errs.NotFound, "model file is missing: %s", fp).
			WithHint("run maio rm %s, then install it again", md.Name))
	}
	record["size"] = st.Size()
	info, ggufErr := gguf.Read(fp)
	if ggufErr == nil {
		record["context_length"] = info.ContextLength
		record["quantization"] = info.Quant
		record["architecture"] = info.Architecture
		record["vision"] = info.Vision
		if mm := findMMProj(md.Dir, fp); mm != "" {
			record["mmproj"] = mm
		}
		hw := hardware.Detect(conf.ModelsRoot)
		extra := mmprojSize(md.Dir, fp)
		budget := vram.Estimate(info, st.Size()+extra, conf.CtxSize, hw)
		record["vram_total"] = budget.Total
		record["vram_free"] = budget.Free
		record["vram_fits"] = budget.Fits()
	}
	if asJSON {
		if ggufErr != nil {
			record["gguf_error"] = ggufErr.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(record)
		if ggufErr != nil {
			return 7
		}
		return 0
	}
	fmt.Printf("name        : %s\nformat      : %s\npath        : %s\nconfig      : %s\nsize        : %s\n",
		md.Name, md.Format, fp, config.ModelConfigPath(md.Dir), humanSize(st.Size()))
	if ggufErr != nil {
		fmt.Printf("gguf        : invalid (%v)\n", ggufErr)
		return fail(ggufErr)
	}
	mm := findMMProj(md.Dir, fp)
	fmt.Printf("architecture: %s\ncontext     : %d\nquantization: %s\nvision      : %s\n",
		info.Architecture, info.ContextLength, info.Quant, boolYes(info.Vision))
	if mm != "" {
		fmt.Printf("mmproj      : %s\n", mm)
	}
	hw := hardware.Detect(conf.ModelsRoot)
	extra := mmprojSize(md.Dir, fp)
	budget := vram.Estimate(info, st.Size()+extra, conf.CtxSize, hw)
	fmt.Printf("gpu budget  : %s\n", budget.Summary())
	if hw.HasGPU() && !budget.Fits() {
		fmt.Printf("              does not fit; %s\n", budget.Suggestion("--ctx"))
	}
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
			return usagef("unknown argument %s", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usagef("usage: maio rm <name> [-y]")
	}
	md, err := registry.Find(conf.ModelsRoot, pos[0])
	if err != nil {
		return fail(err)
	}
	inst, instErr := engine.New(conf).Get(md.Name)
	if instErr != nil {
		return fail(instErr)
	}
	if inst != nil {
		return fail(errs.Newf(errs.Conflict, "model %s is loaded (pid %d)", md.Name, inst.PID).
			WithHint("unload it first with: maio stop %s", md.Name))
	}
	info, err := os.Stat(md.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(errs.Newf(errs.NotFound, "model directory does not exist: %s", md.Dir))
		}
		if os.IsPermission(err) {
			return fail(errs.Wrap(errs.Permission, err, "cannot access model directory %s", md.Dir).
				WithHint("run with sudo, or fix the directory permissions"))
		}
		return fail(errs.Wrap(errs.IO, err, "cannot access model directory %s", md.Dir))
	}
	if !info.IsDir() {
		return fail(errs.Newf(errs.IO, "model path is not a directory: %s", md.Dir))
	}
	var size int64
	_ = filepath.Walk(md.Dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && fi.Mode().IsRegular() {
			size += fi.Size()
		}
		return nil
	})
	if !yes {
		if !isTerminal(os.Stdin) {
			return fail(errs.New(errs.Usage, "confirmation is required in a non-interactive session").
				WithHint("pass -y to delete %s without prompting", md.Name))
		}
		fmt.Printf("delete %s (%s) and all of its files? [y/N] ", md.Name, humanSize(size))
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("cancelled")
			logging.Info("uninstall.cancelled", "model", md.Name)
			return 0
		}
	}
	logging.Info("uninstall.start", "model", md.Name, "path", md.Dir, "bytes", size)
	if err := os.RemoveAll(md.Dir); err != nil {
		logging.Error("uninstall.failed", err, "model", md.Name, "path", md.Dir)
		return fail(errs.Wrap(errs.Permission, err, "cannot delete %s", md.Dir).
			WithHint("check permissions, or run: sudo maio rm %s", md.Name))
	}
	if filepath.IsAbs(md.Path) && !pathWithin(md.Dir, md.Path) {
		fmt.Fprintf(os.Stderr, "maio: warning: the model file is outside the model directory and was not deleted: %s\n", md.Path)
	}
	fmt.Printf("deleted %s\n", md.Dir)
	logging.Info("uninstall.done", "model", md.Name, "path", md.Dir, "bytes", size)
	return 0
}

func pathWithin(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cmdRun(conf *config.Global, args []string) int {
	ctxLen := 0
	force := false
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Println("usage: maio run <name> [prompt] [--ctx N] [--force]")
			return 0
		case "--ctx":
			if i+1 >= len(args) {
				return usagef("--ctx requires a positive integer")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return usagef("invalid --ctx value %q", args[i])
			}
			ctxLen = n
		case "--force":
			force = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return usagef("unknown argument %s", args[i])
			}
			pos = append(pos, args[i])
		}
	}
	if len(pos) == 0 {
		return usagef("usage: maio run <name> [prompt]")
	}
	md, err := registry.Find(conf.ModelsRoot, pos[0])
	if err != nil {
		return fail(err)
	}
	ctx, stop := signalContext()
	defer stop()
	eng := engine.New(conf)
	inst, err := eng.Ensure(ctx, md, engine.Options{Ctx: ctxLen, Force: force})
	if err != nil {
		if ctx.Err() != nil {
			return fail(errs.Wrap(errs.Interrupted, ctx.Err(), "interrupted while loading %s", md.Name))
		}
		return fail(err)
	}
	defer func() {
		if err := eng.Stop(md.Name); err != nil {
			fmt.Fprintf(os.Stderr, "maio: warning: failed to stop the engine: %v\n", err)
			logging.Warn("engine.stop_failed", "model", md.Name, "error", err.Error())
		}
	}()
	logging.Info("run.start", "model", md.Name, "port", inst.Port, "pid", inst.PID)
	if prompt := strings.Join(pos[1:], " "); prompt != "" {
		_, err := chatStream(ctx, inst.Port, md.Name, []chatMessage{{Role: "user", Content: prompt}})
		if err != nil {
			return fail(err)
		}
		return 0
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
		reply, err := chatStream(ctx, inst.Port, md.Name, history)
		if err != nil {
			return fail(err)
		}
		history = append(history, chatMessage{Role: "assistant", Content: reply})
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var chatClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 120 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

func chatStream(ctx context.Context, port int, model string, msgs []chatMessage) (string, error) {
	payload, err := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	if err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot build the chat request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), bytes.NewReader(payload))
	if err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot build the chat request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := chatClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", errs.Wrap(errs.Interrupted, ctx.Err(), "chat interrupted")
		}
		return "", errs.Wrap(errs.Engine, err, "cannot reach the engine on 127.0.0.1:%d", port).
			WithHint("check the engine log with: maio logs --tail 50")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", errs.Newf(errs.Engine, "engine returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
		if ctx.Err() != nil {
			return reply.String(), errs.Wrap(errs.Interrupted, ctx.Err(), "chat interrupted")
		}
		return reply.String(), errs.Wrap(errs.Engine, err, "failed to read the chat stream")
	}
	return reply.String(), nil
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
				return usagef("--host requires a value")
			}
			i++
			host = args[i]
		case "--port":
			if i+1 >= len(args) {
				return usagef("--port requires a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil || p < 1 || p > 65535 {
				return usagef("invalid port: %s", args[i])
			}
			port = p
		default:
			return usagef("unknown argument %s", args[i])
		}
	}
	if strings.TrimSpace(host) == "" {
		return usagef("--host must not be empty")
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if port < 1024 && os.Geteuid() != 0 {
			return fail(errs.Wrap(errs.Permission, err, "cannot listen on %s", addr).
				WithHint("ports below 1024 require root: sudo maio serve --port %d", port))
		}
		return fail(errs.Wrap(errs.Service, err, "cannot listen on %s", addr).
			WithHint("the port may be in use; choose another one with --port, or check with: ss -ltnp"))
	}
	ctx, stop := signalContext()
	defer stop()
	srv := server.New(conf, engine.New(conf), Version)
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	stopIdle := srv.StartIdleUnloader(ctx, server.IdleUnload)
	defer stopIdle()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logging.Warn("serve.shutdown_failed", "error", err.Error())
		}
	}()
	fmt.Printf("maio service started at http://%s (pid %d)\n", addr, os.Getpid())
	logging.Info("serve.start", "addr", addr, "pid", os.Getpid(), "version", Version,
		"models_root", conf.ModelsRoot)
	err = httpSrv.Serve(ln)
	if err != nil && err != http.ErrServerClosed {
		logging.Error("serve.failed", errs.Wrap(errs.Service, err, "service failed"))
		return fail(errs.Wrap(errs.Service, err, "service failed on %s", addr))
	}
	if err := srv.Eng.StopAll(); err != nil {
		logging.Warn("serve.stop_engines_failed", "error", err.Error())
	}
	fmt.Println("maio service stopped")
	logging.Info("serve.stop", "addr", addr)
	return 0
}

func cmdPs(conf *config.Global, args []string) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio ps [--json]")
			return 0
		case "--json":
			asJSON = true
		default:
			return usagef("unknown argument %s", a)
		}
	}
	eng := engine.New(conf)
	insts, err := eng.List()
	if err != nil {
		return fail(err)
	}
	type row struct {
		Name    string `json:"name"`
		Port    int    `json:"port"`
		PID     int    `json:"pid"`
		Status  string `json:"status"`
		Started string `json:"started"`
		Error   string `json:"error,omitempty"`
	}
	rows := make([]row, 0, len(insts))
	for _, in := range insts {
		status := "ok"
		reason := ""
		if err := eng.Health(in.Port); err != nil {
			status = "dead"
			reason = err.Error()
			fmt.Fprintf(os.Stderr, "maio: warning: %s is not responding: %v\n", in.Name, err)
		}
		rows = append(rows, row{Name: in.Name, Port: in.Port, PID: in.PID, Status: status, Started: in.Started, Error: reason})
	}
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Println("(no models loaded)")
		return 0
	}
	fmt.Printf("%-24s %6s %8s %-8s %s\n", "NAME", "PORT", "PID", "STATUS", "STARTED")
	for _, r := range rows {
		fmt.Printf("%-24s %6d %8d %-8s %s\n", r.Name, r.Port, r.PID, r.Status, r.Started)
	}
	return 0
}

func cmdStop(conf *config.Global, args []string) int {
	all := false
	var pos []string
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case a == "-h" || a == "--help":
			fmt.Println("usage: maio stop <name> | maio stop --all")
			return 0
		case strings.HasPrefix(a, "-"):
			return usagef("unknown argument %s", a)
		default:
			pos = append(pos, a)
		}
	}
	eng := engine.New(conf)
	if all {
		fmt.Println("stopping all loaded models")
		if err := eng.StopAll(); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(pos) != 1 {
		return usagef("usage: maio stop <name>")
	}
	name, err := naming.Normalize(pos[0])
	if err != nil {
		return fail(err)
	}
	inst, err := eng.Get(name)
	if err != nil {
		return fail(err)
	}
	if inst == nil {
		fmt.Printf("model %s is not loaded\n", name)
		logging.Info("stop.noop", "model", name)
		return 0
	}
	if err := eng.Stop(name); err != nil {
		return fail(err)
	}
	fmt.Printf("stopped %s\n", name)
	return 0
}

func cmdBoot(conf *config.Global, args []string) int {
	dry := false
	userMode := false
	value := ""
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Println("usage: sudo maio boot 1|0 [--user] [--dry-run]")
			fmt.Println("  1 = start on boot, 0 = manual start")
			return 0
		case a == "--dry-run":
			dry = true
		case a == "--user":
			userMode = true
		case strings.HasPrefix(a, "-"):
			return usagef("unknown argument %s", a)
		default:
			if value != "" {
				return usagef("too many arguments")
			}
			value = a
		}
	}
	if value != "1" && value != "0" {
		return usagef("usage: sudo maio boot 1|0 (1=start on boot, 0=manual start)")
	}
	mode := systemd.System
	if userMode {
		mode = systemd.User
	}
	boot := 0
	if value == "1" {
		boot = 1
	}
	confPath := conf.Source
	if confPath == "" {
		confPath = config.ConfPath()
	}
	if userMode && confPath != config.UserConfPath() {
		confPath = config.UserConfPath()
	}
	exe := installedExe()
	unitPath := systemd.UnitPath(mode)
	if dry {
		action := "enable --now"
		if boot == 0 {
			action = "disable --now"
		}
		flag := modeFlag(mode)
		if flag != "" {
			flag += " "
		}
		fmt.Printf("will set boot = %d\n  config: %s\n  unit: %s\n  run: systemctl %s%s\n",
			boot, confPath, unitPath, flag, action)
		return 0
	}
	if !userMode && os.Geteuid() != 0 {
		return fail(errs.New(errs.Permission, "the system service requires root").
			WithHint("use: sudo maio boot %s  (or: maio boot %s --user for a per-user service)", value, value))
	}
	conf.Boot = boot
	var err error
	if userMode {
		conf.Source = confPath
		err = conf.SaveTo(confPath)
	} else {
		err = conf.Save()
	}
	if err != nil {
		return fail(err)
	}
	logging.Info("boot.save", "boot", boot, "config", confPath, "mode", string(mode))
	if boot == 1 {
		if err := systemd.Enable(mode, exe, confPath); err != nil {
			return fail(err)
		}
		st := systemd.Query(mode)
		if !st.Active {
			return fail(errs.Newf(errs.Service, "the %s maio service was enabled but is not running", mode).
				WithHint("inspect it with: journalctl %s -u maio -n 50; or start it manually with: %s serve",
					modeFlag(mode), exe))
		}
		fmt.Printf("start on boot enabled (%s service, active=%v, enabled=%v)\n", mode, st.Active, st.Enabled)
		logging.Info("boot.enable", "mode", string(mode), "unit", unitPath, "exe", exe)
		if userMode {
			user := currentUser()
			if ok, _ := systemd.Linger(user); !ok {
				fmt.Fprintf(os.Stderr, "maio: note: the per-user service starts after login; for boot-time start run:\n  %s\n", systemd.LingerHint(user))
			}
		}
		return 0
	}
	if err := systemd.Disable(mode); err != nil {
		return fail(err)
	}
	fmt.Printf("start on boot disabled (%s service)\n", mode)
	logging.Info("boot.disable", "mode", string(mode), "unit", unitPath)
	return 0
}

func installedExe() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	if p, err := exec.LookPath("maio"); err == nil {
		return p
	}
	return "maio"
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}

func modeFlag(mode systemd.Mode) string {
	if mode == systemd.User {
		return "--user"
	}
	return ""
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	return "root"
}

func sanitizeArgs(args []string) string {
	redacted := make([]string, 0, len(args))
	hideNext := false
	for _, a := range args {
		if hideNext {
			redacted = append(redacted, stripQuery(a))
			hideNext = false
			continue
		}
		if a == "--url" || a == "--sha256" {
			redacted = append(redacted, a)
			hideNext = true
			continue
		}
		if strings.HasPrefix(a, "--url=") {
			redacted = append(redacted, "--url="+stripQuery(strings.TrimPrefix(a, "--url=")))
			continue
		}
		redacted = append(redacted, a)
	}
	out := strings.Join(redacted, " ")
	if len(out) > 500 {
		out = out[:500] + "..."
	}
	return out
}

func stripQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	u.RawQuery = ""
	return u.String()
}

func mmprojSize(dir, modelFile string) int64 {
	mm := findMMProj(dir, modelFile)
	if mm == "" {
		return 0
	}
	if st, err := os.Stat(mm); err == nil {
		return st.Size()
	}
	return 0
}

func findMMProj(dir, modelFile string) string {
	dir = filepath.Dir(modelFile)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var cands []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			continue
		}
		if !strings.Contains(strings.ToLower(e.Name()), "mmproj") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p != modelFile {
			cands = append(cands, p)
		}
	}
	sort.Strings(cands)
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

func humanSize(n int64) string {
	if n < 0 {
		return "?"
	}
	return hardware.HumanBytes(uint64(n))
}

func boolYes(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func printHelp() {
	fmt.Print(`maio - lightweight model management CLI built on llama.cpp

Usage:
  maio <command> [arguments]
  sudo maio <command> [arguments]    for system-wide changes

Models:
  sudo maio install <name> [root]           install a model into <root>/<name>/
  sudo maio install <name> [root] --url URL install from a direct link
  sudo maio install <name> [root] --url /abs/model.gguf
                                            register an existing local GGUF
  maio list [--wide]                        list installed models
  maio show <name>                          show config, file and capabilities
  maio rm <name> [-y]                       delete a model and its files
  maio run <name> [prompt] [--ctx N] [--force]
                                            chat or run one prompt locally

Service:
  maio serve [--host ADDR] [--port N]       start the HTTP service
  maio ps                                   list loaded models
  maio stop <name> | --all                  unload model(s) and free VRAM
  sudo maio boot 1|0 [--user]               start-on-boot switch (default 1)
  maio boot 1|0 --user                      per-user service (no root)

Diagnostics:
  maio doctor [--json]                      check config, hardware, engine, models
  maio logs [--tail N] [--json] [--path]    read the audit log
  maio version [--json]                     print version and paths

Install maio itself:
  sudo apt install maio-cluster              from the packaged repository
  sudo bash maio-cluster                     from a source checkout

Environment:
  MAIO_CONF   global config path (default /etc/maio/maio.conf or per-user)
  MAIO_LOG    audit log path override
  MAIO_DEBUG  1 = print underlying causes on errors
  MAIO_LANG   reserved for language packs (English for now)

Exit codes:
  0 ok, 2 usage, 3 not found, 4 conflict, 5 permission/io, 6 download,
  7 verification, 8 engine, 9 hardware, 10 config, 11 service, 130 interrupted

Notes:
  - model names are lowercase; spaces become - (DeepSeek v4.1 -> deepseek-v4.1)
  - use "sudo maio ...", not "maio sudo ..."
  - the audit log records installs, downloads, removals and engine actions
`)
}
