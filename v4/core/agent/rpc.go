package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

func (a *Agent) startRPC(ctx context.Context, args map[string]string, data map[string]string) error {
	bin := a.hw.RPCBin
	if bin == "" {
		return errs.New(errs.Unavailable, "ggml-rpc-server is not installed on this node").
			WithHint("install the maio rpc package for this platform, or set agent.rpc_bin")
	}
	want, _ := strconv.Atoi(args["port"])
	if want == 0 {
		want = a.cfg.Agent.RPCPortBase
	}
	a.mu.Lock()
	if p, ok := a.rpcs[want]; ok {
		data["port"] = strconv.Itoa(p.port)
		data["endpoint"] = net.JoinHostPort(advertisedHost(), strconv.Itoa(p.port))
		data["pid"] = strconv.Itoa(p.cmd.Process.Pid)
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	port, err := a.freeRPCPort(want)
	if err != nil {
		return err
	}
	logDir := filepath.Join(a.cfg.Agent.DataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create log directory")
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("rpc-%d.log", port))
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot open RPC log")
	}
	cmd := exec.Command(bin, "-H", "0.0.0.0", "-p", strconv.Itoa(port), "-c")
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = append(os.Environ(),
		"LLAMA_CACHE="+filepath.Join(a.cfg.Agent.DataDir, "rpc-cache"),
	)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		lf.Close()
		return errs.Wrap(errs.Unavailable, err, "cannot start %s", bin)
	}
	p := &rpcProcess{cmd: cmd, port: port, started: time.Now(), logPath: logPath, done: make(chan struct{})}
	a.mu.Lock()
	a.rpcs[port] = p
	a.mu.Unlock()
	go func() {
		err := cmd.Wait()
		lf.Close()
		close(p.done)
		a.mu.Lock()
		delete(a.rpcs, port)
		a.mu.Unlock()
		if err != nil {
			a.log.Warn("agent.rpc_exited", "port", port, "err", err)
		}
	}()

	// Wait until the RPC server accepts TCP connections.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			p.stop()
			return ctx.Err()
		case <-p.done:
			return errs.New(errs.Unavailable, "RPC server exited during startup").
				WithHint("log: %s\n%s", logPath, tailFile(logPath, 1200))
		case <-time.After(150 * time.Millisecond):
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			c.Close()
			data["port"] = strconv.Itoa(port)
			data["endpoint"] = net.JoinHostPort(advertisedHost(), strconv.Itoa(port))
			data["pid"] = strconv.Itoa(cmd.Process.Pid)
			data["devices"] = strconv.Itoa(max(1, len(a.hw.GPUs)))
			return nil
		}
	}
	p.stop()
	return errs.New(errs.Timeout, "RPC server did not listen on port %d", port).
		WithHint("log: %s\n%s", logPath, tailFile(logPath, 1200))
}

func (a *Agent) freeRPCPort(want int) (int, error) {
	for p := want; p < want+64; p++ {
		a.mu.Lock()
		_, used := a.rpcs[p]
		a.mu.Unlock()
		if used {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(p)))
		if err != nil {
			continue
		}
		ln.Close()
		return p, nil
	}
	return 0, errs.New(errs.Unavailable, "no free RPC port in %d..%d", want, want+63)
}

func (a *Agent) stopRPC(args map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if raw := args["port"]; raw != "" {
		p, _ := strconv.Atoi(raw)
		if proc, ok := a.rpcs[p]; ok {
			proc.stop()
			delete(a.rpcs, p)
		}
		return nil
	}
	for port, proc := range a.rpcs {
		proc.stop()
		delete(a.rpcs, port)
	}
	return nil
}

func (p *rpcProcess) stop() {
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

func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := st.Size() - int64(n)
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(buf, off)
	return strings.TrimSpace(string(buf))
}

func advertisedHost() string {
	if h, ok := netinfoBest(); ok {
		return h
	}
	return "127.0.0.1"
}

func (a *Agent) rpcPortSet() map[int]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[int]bool{}
	for p := range a.rpcs {
		out[p] = true
	}
	return out
}

var _ = cluster.Command{}
