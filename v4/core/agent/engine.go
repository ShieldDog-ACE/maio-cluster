package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

type engineProcess struct {
	cmd     *exec.Cmd
	port    int
	model   string
	logPath string
	done    chan struct{}
}

func (a *Agent) startEngine(ctx context.Context, args map[string]string, data map[string]string) error {
	name := args["model"]
	if name == "" {
		return errs.New(errs.Usage, "engine.start requires model")
	}
	entry, err := a.models.Get(name)
	if err != nil {
		return err
	}
	bin := a.hw.LlamaBin
	if bin == "" {
		return errs.New(errs.Unavailable, "llama-server is not installed on this node").
			WithHint("install the maio llama package or set agent.llama_bin")
	}
	port, err := a.freeEnginePort()
	if err != nil {
		return err
	}
	logDir := filepath.Join(a.cfg.Agent.DataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create log directory")
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("engine-%d.log", port))
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot open engine log")
	}
	argv := []string{
		"--model", entry.AbsPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-webui",
		"-ngl", "99",
	}
	if v, _ := strconv.Atoi(args["ctx"]); v > 0 {
		argv = append(argv, "-c", strconv.Itoa(v))
	}
	if v, _ := strconv.Atoi(args["threads"]); v > 0 {
		argv = append(argv, "-t", strconv.Itoa(v))
	}
	cmd := exec.Command(bin, argv...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = engineEnv(bin)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return errs.Wrap(errs.Unavailable, err, "cannot start llama-server")
	}
	p := &engineProcess{cmd: cmd, port: port, model: name, logPath: logPath, done: make(chan struct{})}
	a.mu.Lock()
	if a.engines == nil {
		a.engines = map[int]*engineProcess{}
	}
	a.engines[port] = p
	a.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		lf.Close()
		close(p.done)
		a.mu.Lock()
		if cur, ok := a.engines[port]; ok && cur == p {
			delete(a.engines, port)
		}
		a.mu.Unlock()
	}()

	deadline := time.Now().Add(5 * time.Minute)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			p.stop()
			return ctx.Err()
		case <-p.done:
			return errs.New(errs.Unavailable, "llama-server exited during startup").
				WithHint("log: %s\n%s", logPath, tailFile(logPath, 1600))
		case <-time.After(300 * time.Millisecond):
		}
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusOK {
				data["port"] = strconv.Itoa(port)
				data["endpoint"] = "127.0.0.1:" + strconv.Itoa(port)
				data["pid"] = strconv.Itoa(cmd.Process.Pid)
				data["model"] = name
				return nil
			}
		}
	}
	p.stop()
	return errs.New(errs.Timeout, "llama-server did not become ready").
		WithHint("log: %s\n%s", logPath, tailFile(logPath, 1600))
}

func (a *Agent) stopEngine(args map[string]string) error {
	if raw := args["port"]; raw != "" {
		port, _ := strconv.Atoi(raw)
		a.mu.Lock()
		p := a.engines[port]
		delete(a.engines, port)
		a.mu.Unlock()
		p.stop()
		return nil
	}
	if name := args["model"]; name != "" {
		a.mu.Lock()
		var victims []*engineProcess
		for port, p := range a.engines {
			if p.model == name {
				victims = append(victims, p)
				delete(a.engines, port)
			}
		}
		a.mu.Unlock()
		for _, p := range victims {
			p.stop()
		}
		return nil
	}
	a.mu.Lock()
	for port, p := range a.engines {
		p.stop()
		delete(a.engines, port)
	}
	a.mu.Unlock()
	return nil
}

func (p *engineProcess) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	killProcessGroup(p.cmd)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
	}
}

func (a *Agent) freeEnginePort() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := 0; i < 32; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, errs.Wrap(errs.Network, err, "cannot allocate engine port")
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if _, used := a.engines[port]; !used {
			return port, nil
		}
	}
	return 0, errs.New(errs.Unavailable, "cannot allocate engine port")
}

func engineEnv(bin string) []string {
	env := os.Environ()
	dir := filepath.Dir(bin)
	lib := filepath.Join(dir, "..", "lib")
	sep := string(os.PathListSeparator)
	key := "LD_LIBRARY_PATH"
	if strings.Contains(strings.ToLower(os.Getenv("OS")), "windows") {
		key = "PATH"
	}
	cur := os.Getenv(key)
	parts := []string{dir, lib}
	if cur != "" {
		parts = append(parts, cur)
	}
	return append(env, key+"="+strings.Join(parts, sep))
}
