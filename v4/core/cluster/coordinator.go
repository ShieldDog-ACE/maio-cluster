// Package cluster contains the coordinator, its HTTP API and the client used by
// agents and the CLI.
package cluster

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/market"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/plugin"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// JobRunner executes one job. It is implemented by the inference package and
// injected by the CLI, which keeps cluster state independent from llama.cpp.
type JobRunner interface {
	Run(ctx context.Context, job *Job, emit func(JobEvent)) error
	Cancel(jobID string)
}

// JobEvent is one streamed update.
type JobEvent struct {
	Type   string   `json:"type"` // state, token, done, error
	State  JobState `json:"state,omitempty"`
	Text   string   `json:"text,omitempty"`
	Tokens int      `json:"tokens,omitempty"`
	Error  string   `json:"error,omitempty"`
	Job    *Job     `json:"job,omitempty"`
}

type member struct {
	node    Node
	cmdCh   chan *Command
	pending map[string]chan CommandResult
	load    float64
}

// Coordinator owns membership, placement and job state.
type Coordinator struct {
	cfg     *config.Config
	info    ClusterInfo
	log     *slog.Logger
	started time.Time

	mu            sync.RWMutex
	members       map[string]*member
	jobs          map[string]*Job
	jobSubs       map[string]map[chan JobEvent]struct{}
	jobCancel     map[string]context.CancelFunc
	runner        JobRunner
	server        *http.Server
	listener      net.Listener
	tunnelLn      net.Listener
	tunnelPort    int
	tunnelAllowed map[string]map[int]bool
	done          chan struct{}
	stopped       bool

	// OnJobFinished is called after a job reaches a terminal state.
	OnJobFinished func(job Job)

	// Models is the coordinator-local model cache. It serves model files to
	// workers and supplies GGUF metadata to the planner.
	Models *model.Store

	// Plugins and Market back the package manager and the web console.
	Plugins *plugin.Manager
	Market  *market.Client
}

// NewCoordinator builds a coordinator from validated configuration.
func NewCoordinator(cfg *config.Config, log *slog.Logger) (*Coordinator, error) {
	if cfg.Cluster.Token == "" {
		return nil, errs.New(errs.Auth, "cluster token is empty").
			WithHint("run \"maio init\" to generate a token, then start the cluster")
	}
	if log == nil {
		log = slog.Default()
	}
	c := &Coordinator{
		cfg:       cfg,
		log:       log,
		started:   time.Now(),
		members:   map[string]*member{},
		jobs:      map[string]*Job{},
		jobSubs:   map[string]map[chan JobEvent]struct{}{},
		jobCancel: map[string]context.CancelFunc{},
		done:      make(chan struct{}),
		Plugins:   plugin.NewManager(cfg.Agent.DataDir),
		Market:    market.New(marketBaseURL()),
	}
	c.info = ClusterInfo{
		ID: cfg.Cluster.ID, Name: cfg.Cluster.Name, Version: version.Version,
		Protocol: version.Protocol, Port: cfg.Cluster.Port, StartedAt: c.started,
		TLS: cfg.Security.TLSCert != "",
	}
	c.info.Host = c.advertiseHost()
	return c, nil
}

// SetRunner injects the inference runner.
func (c *Coordinator) SetRunner(r JobRunner) { c.mu.Lock(); c.runner = r; c.mu.Unlock() }

// Info returns the public cluster description.
func (c *Coordinator) Info() ClusterInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info := c.info
	info.Nodes = len(c.members)
	info.Coordinator = c.cfg.Cluster.Name
	info.Host = c.advertiseHost()
	return info
}

// Start binds the API and serves until ctx is cancelled.
func (c *Coordinator) Start(ctx context.Context) error {
	addr := net.JoinHostPort(c.cfg.Cluster.Bind, strconv.Itoa(c.cfg.Cluster.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot listen on %s", addr).
			WithHint("another coordinator may be running; check \"maio status\" or choose another port")
	}
	c.mu.Lock()
	c.listener = ln
	c.info.Port = ln.Addr().(*net.TCPAddr).Port
	c.mu.Unlock()

	c.server = &http.Server{
		Handler:           c.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if c.cfg.Security.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.cfg.Security.TLSCert, c.cfg.Security.TLSKey)
		if err != nil {
			ln.Close()
			return errs.Wrap(errs.Config, err, "cannot load TLS certificate")
		}
		c.server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	if err := c.startTunnelServer(); err != nil {
		c.log.Warn("cluster.tunnel_unavailable", "err", err)
	}
	go c.pruneLoop(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.server.Shutdown(shutdownCtx)
	}()
	c.log.Info("cluster.listening", "addr", ln.Addr().String(), "cluster", c.info.ID)
	if c.cfg.Security.TLSCert != "" {
		err = c.server.ServeTLS(ln, "", "")
	} else {
		err = c.server.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Addr returns the bound API address.
func (c *Coordinator) Addr() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.listener != nil {
		return c.listener.Addr().String()
	}
	return ""
}

// Close wakes waiting clients.
func (c *Coordinator) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.stopped = true
	for _, m := range c.members {
		close(m.cmdCh)
	}
	if c.tunnelLn != nil {
		_ = c.tunnelLn.Close()
	}
	close(c.done)
}

// Nodes returns a sorted snapshot.
func (c *Coordinator) Nodes() []Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Node, 0, len(c.members))
	now := time.Now()
	for _, m := range c.members {
		n := m.node.Clone()
		if n.State != Draining && n.State != Offline {
			if now.Sub(n.LastSeen) > time.Duration(c.cfg.Agent.OfflineSec)*time.Second {
				n.State = Offline
			} else if now.Sub(n.LastSeen) > time.Duration(c.cfg.Agent.HeartbeatSec*3)*time.Second {
				n.State = Stale
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role == "coordinator"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Node returns one node by ID.
func (c *Coordinator) Node(id string) (Node, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.members[id]
	if !ok {
		return Node{}, false
	}
	return m.node.Clone(), true
}

func (c *Coordinator) join(req JoinRequest, remoteIP string) (*JoinResponse, error) {
	if len(req.Node.Name) == 0 || len(req.Node.Name) > 64 {
		return nil, errs.New(errs.Usage, "node name must be 1..64 characters")
	}
	if req.Node.Protocol != version.Protocol {
		return nil, errs.New(errs.Unavailable, "protocol mismatch: node speaks %d, cluster speaks %d", req.Node.Protocol, version.Protocol)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.members) >= c.cfg.Cluster.MaxNodes {
		if _, exists := c.members[req.Node.ID]; !exists {
			return nil, errs.New(errs.Unavailable, "cluster is full (%d nodes)", c.cfg.Cluster.MaxNodes)
		}
	}
	id := req.Node.ID
	if id == "" || !strings.HasPrefix(id, "node_") {
		id = NewID("node_")
	}
	n := req.Node.Clone()
	n.ID = id
	n.ClusterID = c.info.ID
	n.Role = "worker"
	if req.Node.Role == "coordinator" && isLoopback(remoteIP) {
		n.Role = "coordinator"
		n.Compute = c.cfg.Cluster.Compute
	}
	n.State = Online
	n.JoinedAt = time.Now()
	n.LastSeen = time.Now()
	n.APIPort = 0
	if !isPrivate(n.Host) || n.Host == "" {
		n.Host = remoteIP
	}
	if n.Hardware.CapacityBytes() == 0 {
		n.Compute = false
	}
	m, exists := c.members[id]
	if !exists {
		m = &member{cmdCh: make(chan *Command, 8), pending: map[string]chan CommandResult{}}
		c.members[id] = m
	}
	m.node = n
	c.log.Info("cluster.join", "node", id, "name", n.Name, "host", n.Host, "os", n.OS, "rpc", n.RPC.Available)
	return &JoinResponse{
		OK: true, NodeID: id, Cluster: c.info,
		HeartbeatMS: c.cfg.Agent.HeartbeatSec * 1000,
	}, nil
}

func (c *Coordinator) heartbeat(hb Heartbeat) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[hb.NodeID]
	if !ok {
		return errs.New(errs.NotFound, "node %s is not a member; rejoin the cluster", hb.NodeID)
	}
	m.load = hb.Load
	m.node.LastSeen = time.Now()
	if hb.State != "" && hb.State != Offline {
		m.node.State = hb.State
	} else if m.node.State != Draining {
		m.node.State = Online
	}
	if hb.Models != nil {
		m.node.Models = append([]ModelRef(nil), hb.Models...)
	}
	if hb.RPC.Binary != "" || hb.RPC.Available || hb.RPC.Error != "" {
		m.node.RPC = hb.RPC
	}
	if len(hb.Links) > 0 {
		m.node.Links = append([]netinfo.Address(nil), hb.Links...)
	}
	if hb.Host != "" && isPrivate(hb.Host) {
		m.node.Host = hb.Host
	}
	m.node.APIPort = hb.APIPort
	return nil
}

// Call sends one command and waits for its result.
func (c *Coordinator) Call(ctx context.Context, nodeID, kind string, args map[string]string, timeout time.Duration) (CommandResult, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cmd := &Command{
		ID: NewID("cmd_"), Kind: kind, Args: args,
		Created: time.Now(), Timeout: int(timeout.Seconds()),
	}
	resultCh := make(chan CommandResult, 1)
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return CommandResult{}, errs.New(errs.Canceled, "coordinator stopped")
	}
	m, ok := c.members[nodeID]
	if !ok {
		c.mu.Unlock()
		return CommandResult{}, errs.New(errs.NotFound, "node %s is not a member", nodeID)
	}
	if m.node.State == Offline {
		name := m.node.Name
		c.mu.Unlock()
		return CommandResult{}, errs.New(errs.Unavailable, "node %s is offline", name)
	}
	name := m.node.Name
	m.pending[cmd.ID] = resultCh
	select {
	case m.cmdCh <- cmd:
	default:
		delete(m.pending, cmd.ID)
		c.mu.Unlock()
		return CommandResult{}, errs.New(errs.Unavailable, "node %s already has queued work", name)
	}
	c.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-resultCh:
		if !res.OK {
			return res, errs.New(errs.Unavailable, "node %s: %s", name, res.Error)
		}
		return res, nil
	case <-ctx.Done():
		c.dropPending(nodeID, cmd.ID)
		return CommandResult{}, errs.Wrap(errs.Canceled, ctx.Err(), "command cancelled")
	case <-timer.C:
		c.dropPending(nodeID, cmd.ID)
		return CommandResult{}, errs.New(errs.Timeout, "node %s did not answer %s within %s", name, kind, timeout)
	case <-c.done:
		return CommandResult{}, errs.New(errs.Canceled, "coordinator stopped")
	}
}

func (c *Coordinator) dropPending(nodeID, cmdID string) {
	c.mu.Lock()
	if m, ok := c.members[nodeID]; ok {
		delete(m.pending, cmdID)
	}
	c.mu.Unlock()
}

func (c *Coordinator) nextCommand(ctx context.Context, nodeID string) (*Command, error) {
	c.mu.Lock()
	m, ok := c.members[nodeID]
	if !ok {
		c.mu.Unlock()
		return nil, errs.New(errs.NotFound, "not a member")
	}
	m.node.LastSeen = time.Now()
	ch := m.cmdCh
	c.mu.Unlock()
	select {
	case cmd := <-ch:
		if cmd == nil {
			return nil, errs.New(errs.Canceled, "node removed")
		}
		return cmd, nil
	case <-time.After(10 * time.Second):
		return nil, nil
	case <-ctx.Done():
		return nil, nil
	case <-c.done:
		return nil, nil
	}
}

func (c *Coordinator) deliverResult(nodeID string, res CommandResult) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[nodeID]
	if !ok {
		return false
	}
	if ch, exists := m.pending[res.ID]; exists {
		delete(m.pending, res.ID)
		ch <- res
		return true
	}
	return false
}

func (c *Coordinator) pruneLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.prune()
		}
	}
}

func (c *Coordinator) prune() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for id, m := range c.members {
		age := now.Sub(m.node.LastSeen)
		switch {
		case age > 10*time.Minute:
			delete(c.members, id)
			close(m.cmdCh)
			for cmdID, ch := range m.pending {
				ch <- CommandResult{ID: cmdID, Error: "node left the cluster"}
			}
		case age > time.Duration(c.cfg.Agent.OfflineSec)*time.Second:
			if m.node.State != Offline && m.node.State != Draining {
				m.node.State = Offline
				c.log.Warn("cluster.node_offline", "node", m.node.Name, "id", id)
				for cmdID, ch := range m.pending {
					ch <- CommandResult{ID: cmdID, Error: "node went offline"}
					delete(m.pending, cmdID)
				}
			}
		case age > time.Duration(c.cfg.Agent.HeartbeatSec*3)*time.Second:
			if m.node.State == Online {
				m.node.State = Stale
			}
		}
	}
	for id, job := range c.jobs {
		if job.State == JobDone || job.State == JobFailed || job.State == JobCanceled {
			if now.Sub(job.EndedAt) > 30*time.Minute {
				delete(c.jobs, id)
				delete(c.jobSubs, id)
			}
		}
	}
}

func (c *Coordinator) advertiseHost() string {
	if c.cfg.Cluster.Advertise != "" {
		return c.cfg.Cluster.Advertise
	}
	if ip := net.ParseIP(c.cfg.Cluster.Bind); ip != nil && !ip.IsUnspecified() {
		return ip.String()
	}
	if a, ok := netinfo.BestAdvertise(); ok {
		return a.IP
	}
	return "127.0.0.1"
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isPrivate(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}
