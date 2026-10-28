package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// Agent is the worker daemon. It joins one coordinator, heartbeats, executes
// commands and owns the local ggml-rpc-server processes.
type Agent struct {
	cfg     *config.Config
	log     *slog.Logger
	models  *model.Store
	hw      hardware.Info
	sampler *hardware.Sampler

	client   *cluster.Client
	nodeID   string
	joinCode *cluster.JoinCode

	mu               sync.Mutex
	rpcs             map[int]*rpcProcess
	engines          map[int]*engineProcess
	tunnels          map[int]*tunnel
	joined           bool
	localCoordinator bool
	started          time.Time
	shutdown         context.CancelFunc
}

type rpcProcess struct {
	cmd     *exec.Cmd
	port    int
	started time.Time
	logPath string
	done    chan struct{}
}

// New builds an agent.
func New(cfg *config.Config, log *slog.Logger) (*Agent, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(cfg.Agent.ModelsDir, 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create models directory %s", cfg.Agent.ModelsDir)
	}
	store := model.NewStore(cfg.Agent.ModelsDir)
	store.Token = cfg.Cluster.Token
	hw := hardware.Detect(cfg.Agent.ModelsDir)
	hw.RPCBin = findRPCBinary(cfg.Agent.RPCBin)
	hw.LlamaBin = findLlamaBinary(cfg.Agent.LlamaBin)
	return &Agent{
		cfg: cfg, log: log, models: store, hw: hw, sampler: hardware.NewSampler(hw),
		rpcs: map[int]*rpcProcess{}, engines: map[int]*engineProcess{}, tunnels: map[int]*tunnel{},
		started: time.Now(),
	}, nil
}

// MarkCoordinator makes this agent register as the cluster coordinator. The
// coordinator API only accepts that role from a loopback peer.
func (a *Agent) MarkCoordinator() { a.localCoordinator = true }

// NodeID returns the registered node ID (empty before join).
func (a *Agent) NodeID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nodeID
}

// Run joins the coordinator and serves until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.shutdown = cancel
	defer cancel()

	link, err := a.resolveCoordinator(ctx)
	if err != nil {
		return err
	}
	a.client = cluster.NewClient(link.BaseURL, a.cfg.Cluster.Token)
	a.joinCode = link.Code
	if err := a.join(ctx); err != nil {
		return err
	}
	go a.heartbeatLoop(ctx)
	go a.commandLoop(ctx)
	a.log.Info("agent.joined", "cluster", link.Code.ClusterID, "coordinator", link.BaseURL, "node", a.NodeID())
	<-ctx.Done()
	a.closeAll()
	return nil
}

type link struct {
	BaseURL string
	Code    *cluster.JoinCode
}

func (a *Agent) resolveCoordinator(ctx context.Context) (link, error) {
	// 1. Explicit seeds from configuration.
	for _, seed := range a.cfg.Cluster.Seeds {
		base := seed
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		c := cluster.NewClient(base, a.cfg.Cluster.Token)
		if err := c.Health(ctx); err == nil {
			return link{BaseURL: c.BaseURL, Code: &cluster.JoinCode{ClusterID: a.cfg.Cluster.ID}}, nil
		}
	}
	// 2. A saved link from the last successful join.
	if l, ok := loadLink(a.cfg.Agent.DataDir); ok {
		c := cluster.NewClient(l.BaseURL, a.cfg.Cluster.Token)
		if err := c.Health(ctx); err == nil {
			return l, nil
		}
	}
	// 3. Broadcast discovery.
	if a.cfg.Cluster.Discovery {
		port := discoveryPort(a.cfg.Cluster.Port)
		fctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		b, err := discoveryFind(fctx, a.cfg.Cluster.Token, port)
		if err == nil {
			base := fmt.Sprintf("http://%s", net.JoinHostPort(b.Host, fmt.Sprint(b.Port)))
			return link{BaseURL: base, Code: &cluster.JoinCode{
				ClusterID: b.ClusterID, Name: b.Name, Host: b.Host, Port: b.Port,
			}}, nil
		}
	}
	return link{}, errs.New(errs.Network, "no coordinator found").
		WithHint("start one with \"maio cluster up\", or join with \"maio cluster join <code>\"").
		WithHint("seeds: %v", a.cfg.Cluster.Seeds)
}

func (a *Agent) join(ctx context.Context) error {
	var id string
	if st, ok := loadNodeState(a.cfg.Agent.DataDir); ok {
		id = st.ID
	}
	node := cluster.DefaultNode()
	node.ID = id
	node.Name = a.cfg.Agent.Name
	node.Role = "worker"
	if a.localCoordinator {
		node.Role = "coordinator"
		node.APIPort = a.cfg.Cluster.Port
	}
	node.OS = runtime.GOOS
	node.Arch = runtime.GOARCH
	node.Version = version.Version
	node.Protocol = version.Protocol
	node.Hardware = a.hw
	node.Metrics = a.sampler.Sample()
	node.Compute = a.cfg.Agent.MaxJobs > 0
	node.RPC = a.rpcStatus()
	if h, ok := netinfo.BestAdvertise(); ok {
		node.Host = h.IP
		node.Links = []netinfo.Address{h}
	}
	node.Models = a.modelRefs()
	node.JoinedAt = time.Now()
	node.LastSeen = time.Now()

	for attempt := 0; attempt < 3; attempt++ {
		resp, err := a.client.Join(ctx, cluster.JoinRequest{Node: node})
		if err == nil {
			a.models.Token = a.cfg.Cluster.Token
			a.models.TokenHosts = []string{hostOf(a.client.BaseURL)}
			if resp.Cluster.Host != "" {
				a.models.TokenHosts = append(a.models.TokenHosts, resp.Cluster.Host)
			}
			a.mu.Lock()
			a.nodeID = resp.NodeID
			a.joined = true
			a.mu.Unlock()
			saveNodeState(a.cfg.Agent.DataDir, nodeState{ID: resp.NodeID, Coordinator: a.client.BaseURL})
			saveLink(a.cfg.Agent.DataDir, link{BaseURL: a.client.BaseURL, Code: &cluster.JoinCode{
				ClusterID: resp.Cluster.ID, Name: resp.Cluster.Name,
				Host: hostOf(a.client.BaseURL), Port: resp.Cluster.Port,
			}})
			return nil
		}
		a.log.Warn("agent.join_failed", "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return errs.New(errs.Network, "cannot join coordinator at %s", a.client.BaseURL).
		WithHint("check the token and that the coordinator is reachable")
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	interval := time.Duration(a.cfg.Agent.HeartbeatSec) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hb := cluster.Heartbeat{
				NodeID: a.NodeID(), State: cluster.Online,
				Models: a.modelRefs(), RPC: a.rpcStatus(),
				Load: a.load(), Metrics: a.sampler.Sample(),
			}
			if h, ok := netinfo.BestAdvertise(); ok {
				hb.Host = h.IP
				hb.Links = []netinfo.Address{h}
			}
			hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := a.client.Heartbeat(hctx, hb)
			cancel()
			if err != nil {
				a.log.Warn("agent.heartbeat_failed", "err", err)
			}
		}
	}
}

func (a *Agent) load() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return float64(len(a.rpcs)) / float64(max(1, a.cfg.Agent.MaxJobs))
}

func (a *Agent) commandLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cmd, err := a.client.NextCommand(pollCtx, a.NodeID())
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("agent.poll_failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if cmd == nil {
			continue
		}
		res := a.execute(ctx, cmd)
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = a.client.CommandResult(rctx, a.NodeID(), res)
		rcancel()
		if err != nil {
			a.log.Warn("agent.result_failed", "cmd", cmd.Kind, "err", err)
		}
	}
}

func (a *Agent) execute(ctx context.Context, cmd *cluster.Command) cluster.CommandResult {
	res := cluster.CommandResult{ID: cmd.ID, Data: map[string]string{}}
	var err error
	switch cmd.Kind {
	case "ping":
		res.Data["pong"] = "1"
	case "rpc.start":
		err = a.startRPC(ctx, cmd.Args, res.Data)
	case "rpc.stop":
		err = a.stopRPC(cmd.Args)
	case "rpc.list":
		res.Data["ports"] = a.rpcPorts()
	case "engine.start":
		err = a.startEngine(ctx, cmd.Args, res.Data)
	case "engine.stop":
		err = a.stopEngine(cmd.Args)
	case "tunnel.open":
		err = a.openTunnel(ctx, cmd.Args, res.Data)
	case "tunnel.close":
		a.closeTunnelArg(cmd.Args)
	case "model.prepare":
		err = a.prepareModel(ctx, cmd.Args, res.Data)
	case "model.remove":
		err = a.models.Remove(cmd.Args["name"])
	case "node.info":
		raw, _ := json.Marshal(a.hw)
		res.Data["hardware"] = string(raw)
	default:
		err = errs.New(errs.Usage, "unknown command %q", cmd.Kind)
	}
	if err != nil {
		res.OK = false
		res.Error = err.Error()
		a.log.Warn("agent.command_failed", "kind", cmd.Kind, "err", err)
		return res
	}
	res.OK = true
	a.log.Info("agent.command_ok", "kind", cmd.Kind)
	return res
}

func (a *Agent) rpcStatus() cluster.RPCStatus {
	bin := a.hw.RPCBin
	if bin == "" {
		return cluster.RPCStatus{Available: false, Error: "ggml-rpc-server not found"}
	}
	st := cluster.RPCStatus{Available: true, Binary: bin, Devices: len(a.hw.GPUs)}
	if st.Devices == 0 {
		st.Devices = 1
	}
	if out, err := hardware.RunOutput(bin, "--version"); err == nil {
		st.Version = firstLine(out)
	}
	return st
}

func (a *Agent) modelRefs() []cluster.ModelRef {
	entries, _ := a.models.List()
	out := make([]cluster.ModelRef, 0, len(entries))
	for _, e := range entries {
		ref := cluster.ModelRef{Name: e.Name, Size: e.Size}
		if e.Meta != nil {
			ref.Layers = e.Meta.Layers
			ref.Quant = e.Meta.Quant
		}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *Agent) prepareModel(ctx context.Context, args map[string]string, data map[string]string) error {
	name := args["name"]
	if name == "" {
		return errs.New(errs.Usage, "model.prepare requires name")
	}
	url := args["url"]
	if url == "" {
		return errs.New(errs.Usage, "model.prepare requires url")
	}
	if a.models.Has(name) {
		data["path"] = a.cfg.Agent.ModelsDir
		return nil
	}
	e, err := a.models.Install(name, url, args["sha256"], nil)
	if err != nil {
		return err
	}
	data["path"] = e.AbsPath
	data["name"] = e.Name
	data["size"] = fmt.Sprint(e.Size)
	return nil
}

func (a *Agent) closeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.tunnels {
		t.close()
	}
	a.tunnels = map[int]*tunnel{}
	for port, p := range a.rpcs {
		p.stop()
		delete(a.rpcs, port)
	}
	for port, p := range a.engines {
		p.stop()
		delete(a.engines, port)
	}
}

// RPCPorts returns active RPC ports (for diagnostics).
func (a *Agent) RPCPorts() []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]int, 0, len(a.rpcs))
	for p := range a.rpcs {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func (a *Agent) rpcPorts() string {
	ports := a.RPCPorts()
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ",")
}

func findRPCBinary(configured string) string {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates,
		"ggml-rpc-server", "rpc-server",
		filepath.Join(installDir(), "bin", "ggml-rpc-server"),
		"/usr/lib/maio/bin/ggml-rpc-server",
		"/opt/llama.cpp/build/bin/ggml-rpc-server",
		"/usr/lib/maio/llama/ggml-rpc-server",
	)
	for _, c := range candidates {
		if c == "" {
			continue
		}
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

func findLlamaBinary(configured string) string {
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates,
		"llama-server",
		filepath.Join(installDir(), "bin", "llama-server"),
		"/usr/lib/maio/bin/llama-server",
		"/opt/llama.cpp/build/bin/llama-server",
	)
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

func installDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.TrimSpace(s)
}

func hostOf(base string) string {
	base = strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	if h, _, err := net.SplitHostPort(base); err == nil {
		return h
	}
	return base
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
