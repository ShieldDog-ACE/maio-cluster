package inference

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
)

// Runner implements cluster.JobRunner.
type Runner struct {
	cfg   *config.Config
	store *model.Store
	coord *cluster.Coordinator
	log   *slog.Logger

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

// NewRunner builds a runner.
func NewRunner(cfg *config.Config, store *model.Store, coord *cluster.Coordinator, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{cfg: cfg, store: store, coord: coord, log: log, cancel: map[string]context.CancelFunc{}}
}

// Cancel stops a running job.
func (r *Runner) Cancel(id string) {
	r.mu.Lock()
	c := r.cancel[id]
	r.mu.Unlock()
	if c != nil {
		c()
	}
}

// Run executes one job.
func (r *Runner) Run(parent context.Context, job *cluster.Job, emit func(cluster.JobEvent)) error {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.cancel[job.ID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.cancel, job.ID)
		r.mu.Unlock()
	}()

	entry, err := r.store.Get(job.Model)
	if err != nil {
		return err
	}
	meta := cluster.ModelMeta{Name: entry.Name, Size: entry.Size, Context: r.cfg.Llama.Context}
	if entry.Meta != nil {
		meta.Layers = entry.Meta.Layers
		meta.Context = entry.Meta.Context
		meta.Quant = entry.Meta.Quant
	}
	if meta.Context == 0 {
		meta.Context = 4096
	}
	if job.Mode == "" || job.Mode == "auto" {
		job.Mode = r.pickMode(meta)
	}
	r.log.Info("inference.start", "job", job.ID, "model", job.Model, "mode", job.Mode)
	switch job.Mode {
	case "local":
		return r.runLocal(ctx, entry, job, emit)
	case "route":
		return r.runRoute(ctx, entry, job, emit)
	case "shard":
		return r.runShard(ctx, entry, meta, job, emit)
	default:
		return errs.New(errs.Usage, "unknown mode %q", job.Mode)
	}
}

func (r *Runner) pickMode(meta cluster.ModelMeta) string {
	nodes := r.coord.Nodes()
	rpc := 0
	remoteHasModel := false
	localID := r.coord.CoordinatorNodeID()
	for _, n := range nodes {
		if n.State != cluster.Online || !n.Compute {
			continue
		}
		if n.RPC.Available {
			rpc++
		}
		if n.ID != localID && n.HasModel(meta.Name) {
			remoteHasModel = true
		}
	}
	if rpc >= 2 {
		return "shard"
	}
	if remoteHasModel {
		return "route"
	}
	return "local"
}

func (r *Runner) runLocal(ctx context.Context, entry *model.Entry, job *cluster.Job, emit func(cluster.JobEvent)) error {
	bin := findLlama(r.cfg.Agent.LlamaBin)
	if bin == "" {
		bin = findLlama(r.cfg.Llama.ServerBin)
	}
	args := r.localArgs()
	s, err := startServer(bin, entry.AbsPath, args, r.cfg.Agent.DataDir+"/logs", job.ID)
	if err != nil {
		return err
	}
	defer s.stop()
	if err := s.waitReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	_, err = streamCompletion(ctx, s.baseURL(), job.Prompt, maxTokens(job), func(text string, n int) error {
		emit(cluster.JobEvent{Type: "token", Text: text, Tokens: n})
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

func (r *Runner) localArgs() []string {
	args := []string{}
	if r.cfg.Llama.GPULayers > 0 {
		args = append(args, "-ngl", strconv.Itoa(r.cfg.Llama.GPULayers))
	}
	if r.cfg.Llama.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(r.cfg.Llama.Threads))
	}
	if r.cfg.Llama.Context > 0 {
		args = append(args, "-c", strconv.Itoa(r.cfg.Llama.Context))
	}
	args = append(args, r.cfg.Llama.ExtraArgs...)
	return args
}

func (r *Runner) runShard(ctx context.Context, entry *model.Entry, meta cluster.ModelMeta, job *cluster.Job, emit func(cluster.JobEvent)) error {
	localID := r.coord.CoordinatorNodeID()
	plan, err := cluster.BuildPlan(meta, r.coord.Nodes(), cluster.PlanOptions{
		Frontend: localID, MaxNodes: 8, RequireRPC: true,
	})
	if err != nil {
		return err
	}
	r.coord.AttachPlan(job.ID, plan)
	cleanup := &cleanupList{r: r}
	defer cleanup.run()

	var endpoints []string
	var devices []string
	var splits []string
	for i := range plan.Placements {
		p := &plan.Placements[i]
		res, err := r.coord.Call(ctx, p.NodeID, "rpc.start", map[string]string{"port": "0"}, 30*time.Second)
		if err != nil {
			return errs.Wrap(errs.Unavailable, err, "cannot start RPC on %s", p.NodeName)
		}
		port, _ := strconv.Atoi(res.Data["port"])
		if port == 0 {
			return errs.New(errs.Unavailable, "%s returned no RPC port", p.NodeName)
		}
		p.RPCPort = port
		endpoint := res.Data["endpoint"]
		if endpoint == "" {
			endpoint = net.JoinHostPort(p.Host, strconv.Itoa(port))
		}
		if forceTunnel() || !reachable(endpoint) {
			r.coord.AllowTunnel(p.NodeID, port)
			tres, err := r.coord.Call(ctx, p.NodeID, "tunnel.open", map[string]string{
				"target_port": strconv.Itoa(port),
			}, 20*time.Second)
			if err != nil {
				return errs.Wrap(errs.Network, err, "cannot reach %s directly or through a reverse tunnel", p.NodeName)
			}
			endpoint = tres.Data["endpoint"]
			p.Tunneled = true
		}
		p.Endpoint = endpoint
		endpoints = append(endpoints, endpoint)
		devices = append(devices, fmt.Sprintf("RPC%d", i))
		splits = append(splits, strconv.Itoa(p.Layers))
		cleanup.addRPC(p.NodeID, port, p.Tunneled)
	}

	bin := findLlama(r.cfg.Agent.LlamaBin)
	if bin == "" {
		bin = findLlama(r.cfg.Llama.ServerBin)
	}
	args := []string{
		"--rpc", strings.Join(endpoints, ","),
		"--device", strings.Join(devices, ","),
		"-ts", strings.Join(splits, ","),
		"-ngl", "99",
	}
	if meta.Context > 0 {
		args = append(args, "-c", strconv.Itoa(meta.Context))
	}
	if r.cfg.Llama.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(r.cfg.Llama.Threads))
	}
	args = append(args, r.cfg.Llama.ExtraArgs...)
	s, err := startServer(bin, entry.AbsPath, args, r.cfg.Agent.DataDir+"/logs", job.ID)
	if err != nil {
		return err
	}
	defer s.stop()
	if err := s.waitReady(ctx, 15*time.Minute); err != nil {
		return err
	}
	_, err = streamCompletion(ctx, s.baseURL(), job.Prompt, maxTokens(job), func(text string, n int) error {
		emit(cluster.JobEvent{Type: "token", Text: text, Tokens: n})
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

func (r *Runner) runRoute(ctx context.Context, entry *model.Entry, job *cluster.Job, emit func(cluster.JobEvent)) error {
	localID := r.coord.CoordinatorNodeID()
	var target *cluster.Node
	for _, n := range r.coord.Nodes() {
		if n.State != cluster.Online || n.ID == localID || !n.HasModel(entry.Name) {
			continue
		}
		if target == nil || n.Capacity() > target.Capacity() {
			cp := n
			target = &cp
		}
	}
	if target == nil {
		return errs.New(errs.Unavailable, "no online node has model %s", entry.Name).
			WithHint("copy it with \"maio cluster distribute %s\" or install it on a worker", entry.Name)
	}
	res, err := r.coord.Call(ctx, target.ID, "engine.start", map[string]string{
		"model":   entry.Name,
		"ctx":     strconv.Itoa(r.cfg.Llama.Context),
		"threads": strconv.Itoa(r.cfg.Llama.Threads),
	}, 5*time.Minute)
	if err != nil {
		return errs.Wrap(errs.Unavailable, err, "cannot start engine on %s", target.Name)
	}
	port, _ := strconv.Atoi(res.Data["port"])
	cleanup := &cleanupList{r: r}
	defer cleanup.run()
	cleanup.addEngine(target.ID, port)
	endpoint := res.Data["endpoint"]
	if endpoint == "" {
		endpoint = net.JoinHostPort(target.Host, strconv.Itoa(port))
	}
	if forceTunnel() || !reachable(endpoint) {
		r.coord.AllowTunnel(target.ID, port)
		tres, err := r.coord.Call(ctx, target.ID, "tunnel.open", map[string]string{
			"target_port": strconv.Itoa(port),
		}, 20*time.Second)
		if err != nil {
			return errs.Wrap(errs.Network, err, "cannot reach engine on %s", target.Name)
		}
		endpoint = tres.Data["endpoint"]
	}
	_, err = streamCompletion(ctx, "http://"+endpoint, job.Prompt, maxTokens(job), func(text string, n int) error {
		emit(cluster.JobEvent{Type: "token", Text: text, Tokens: n})
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil
	}
	return err
}

func maxTokens(job *cluster.Job) int {
	if job == nil || job.MaxTokens <= 0 {
		return 256
	}
	return job.MaxTokens
}

func forceTunnel() bool {
	v := strings.TrimSpace(os.Getenv("MAIO_FORCE_TUNNEL"))
	return v == "1" || strings.EqualFold(v, "true")
}

func reachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

type cleanupList struct {
	r     *Runner
	once  sync.Once
	items []func(context.Context)
}

func (c *cleanupList) addRPC(nodeID string, port int, tunneled bool) {
	c.items = append(c.items, func(ctx context.Context) {
		if tunneled {
			_, _ = c.r.coord.Call(ctx, nodeID, "tunnel.close", map[string]string{"target_port": strconv.Itoa(port)}, 5*time.Second)
		}
		_, _ = c.r.coord.Call(ctx, nodeID, "rpc.stop", map[string]string{"port": strconv.Itoa(port)}, 5*time.Second)
	})
}

func (c *cleanupList) addEngine(nodeID string, port int) {
	c.items = append(c.items, func(ctx context.Context) {
		_, _ = c.r.coord.Call(ctx, nodeID, "engine.stop", map[string]string{"port": strconv.Itoa(port)}, 5*time.Second)
	})
}

func (c *cleanupList) run() {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for i := len(c.items) - 1; i >= 0; i-- {
			c.items[i](ctx)
		}
	})
}
