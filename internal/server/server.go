// Package server implements maio's HTTP service. It provides an
// Ollama-compatible API plus the OpenAI-compatible endpoints served by
// llama.cpp, with authentication, rate limiting, bounded admission control,
// health, metrics and proactive engine supervision.
package server

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/auth"
	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/engine"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/metrics"
	"github.com/ShieldDog-ACE/maio-cluster/internal/queue"
	"github.com/ShieldDog-ACE/maio-cluster/internal/ratelimit"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
	"github.com/ShieldDog-ACE/maio-cluster/internal/resident"
)

// IdleUnload is the V2 default. V3 reads the value from conf.IdleUnload.
const IdleUnload = 5 * time.Minute

// Server is the maio HTTP service.
type Server struct {
	Conf    *config.Global
	Eng     *engine.Manager
	Version string

	Auth    *auth.Authenticator
	Metrics *metrics.Metrics
	Limiter *ratelimit.Limiter
	Queue   *queue.Limiter

	client    *http.Client
	start     time.Time
	reqSeq    atomic.Uint64
	shutting  atomic.Bool
	accessLog bool

	mu         sync.Mutex
	lastUse    map[string]time.Time
	active     map[string]int
	loaded     map[string]bool
	restarting map[string]bool
	attempts   map[string]int
	nextTry    map[string]time.Time

	residentMu sync.Mutex
	resident   map[string]bool
}

// New creates the service. Authentication is resolved here: a listener on a
// non-loopback address gets a generated key if none is configured.
func New(conf *config.Global, eng *engine.Manager, version string) (*Server, error) {
	if conf == nil {
		return nil, errs.New(errs.Internal, "server config is nil")
	}
	if eng == nil {
		return nil, errs.New(errs.Internal, "engine manager is nil")
	}
	a, err := auth.Load(auth.Options{
		Mode:          conf.Auth,
		Key:           conf.APIKey,
		KeyFile:       conf.AuthKeyFile(),
		Host:          conf.Host,
		AllowInsecure: conf.AllowInsecure == 1,
	})
	if err != nil {
		return nil, err
	}
	m := metrics.New(version)
	s := &Server{
		Conf:       conf,
		Eng:        eng,
		Version:    version,
		Auth:       a,
		Metrics:    m,
		Limiter:    ratelimit.New(conf.RateLimit, conf.RateBurst),
		Queue:      queue.New(conf.MaxConcurrency, conf.MaxQueue, time.Duration(conf.QueueTimeout)*time.Second),
		client:     newUpstreamClient(),
		start:      time.Now(),
		accessLog:  conf.AccessLog == 1,
		lastUse:    make(map[string]time.Time),
		active:     make(map[string]int),
		loaded:     make(map[string]bool),
		restarting: make(map[string]bool),
		attempts:   make(map[string]int),
		nextTry:    make(map[string]time.Time),
		resident:   make(map[string]bool),
	}
	names, rerr := resident.Load(conf.RunDir())
	if rerr != nil {
		logging.Warn("resident.load_failed", "error", rerr.Error())
	}
	for _, n := range names {
		s.resident[n] = true
	}
	if a.Info().Generated {
		logging.Info("auth.key_generated", "path", a.Info().KeyFile, "mode", conf.Auth,
			"hint", "run: sudo maio token --show")
	}
	s.Metrics.SetModels(0, len(s.resident))
	s.Metrics.SetQueue(s.Queue.Depth(), s.Queue.MaxQueue())
	return s, nil
}

// AuthInfo returns non-secret authentication metadata for startup output.
func (s *Server) AuthInfo() auth.Info { return s.Auth.Info() }

// Handler returns the fully wrapped router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/tags", s.handleTags)
	mux.HandleFunc("/api/ps", s.handlePs)
	mux.HandleFunc("/api/show", s.handleShow)
	mux.HandleFunc("/api/generate", s.handleGenerate)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/embed", s.handleEmbed)
	mux.HandleFunc("/api/embeddings", s.handleEmbeddings)
	mux.HandleFunc("/api/load", s.handleLoad)
	mux.HandleFunc("/api/unload", s.handleUnload)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/v1/models", s.handleV1Models)
	mux.HandleFunc("/v1/chat/completions", s.handleV1Proxy)
	mux.HandleFunc("/v1/completions", s.handleV1Proxy)
	mux.HandleFunc("/v1/embeddings", s.handleV1Proxy)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/metrics", s.Metrics.Handler().ServeHTTP)
	return s.wrap(mux)
}

// Start launches the background loops. It returns immediately. Call Shutdown
// on a graceful stop.
func (s *Server) Start(ctx context.Context) {
	if n, err := s.Eng.CleanupStale(); err != nil {
		logging.Warn("engine.cleanup_failed", "error", err.Error())
	} else if n > 0 {
		logging.Info("engine.cleanup_done", "removed", n)
	}
	if s.Conf.Restore == 1 {
		go s.restoreResident(ctx)
	}
	if s.Conf.IdleUnload > 0 {
		go s.idleLoop(ctx, time.Duration(s.Conf.IdleUnload)*time.Second)
	}
	go s.superviseLoop(ctx)
	go s.metricsLoop(ctx)
}

// Shutdown releases all engines and marks the service unavailable. The HTTP
// listener is drained by the caller before this runs.
func (s *Server) Shutdown(ctx context.Context) {
	s.shutting.Store(true)
	done := make(chan error, 1)
	go func() { done <- s.Eng.StopAll() }()
	select {
	case err := <-done:
		if err != nil {
			logging.Warn("serve.stop_engines_failed", "error", err.Error())
		}
	case <-ctx.Done():
		logging.Warn("serve.stop_engines_timeout", "error", ctx.Err().Error())
	}
	s.mu.Lock()
	s.loaded = make(map[string]bool)
	s.active = make(map[string]int)
	s.mu.Unlock()
	s.Metrics.SetModels(0, s.residentCount())
}

// Uptime reports how long this server process has been serving.
func (s *Server) Uptime() time.Duration { return time.Since(s.start) }

// engineOptions maps global config onto one engine start.
func (s *Server) engineOptions() engine.Options {
	return engine.Options{
		Ctx:          s.Conf.CtxSize,
		ContextFlag:  "ctx_size",
		Parallel:     s.Conf.Parallel,
		KVType:       s.Conf.KVType,
		Threads:      s.Conf.Threads,
		FlashAttn:    s.Conf.FlashAttn,
		NoMmap:       s.Conf.Mmap == 0,
		LogVerbosity: s.Conf.LogVerbosity,
	}
}

// ensure loads a model if needed and marks it used.
func (s *Server) ensure(ctx context.Context, name string) (*config.Model, *engine.Instance, error) {
	md, err := registry.Find(s.Conf.ModelsRoot, name)
	if err != nil {
		return nil, nil, err
	}
	inst, err := s.Eng.Ensure(ctx, md, s.engineOptions())
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	s.loaded[md.Name] = true
	s.lastUse[md.Name] = time.Now()
	loaded := len(s.loaded)
	s.mu.Unlock()
	s.Metrics.SetModels(loaded, s.residentCount())
	return md, inst, nil
}

func (s *Server) touch(name string) {
	s.mu.Lock()
	s.lastUse[name] = time.Now()
	s.mu.Unlock()
}

func (s *Server) acquireModel(name string) {
	s.mu.Lock()
	s.active[name]++
	s.lastUse[name] = time.Now()
	s.mu.Unlock()
}

func (s *Server) releaseModel(name string) {
	s.mu.Lock()
	if s.active[name] > 0 {
		s.active[name]--
	}
	s.lastUse[name] = time.Now()
	s.mu.Unlock()
}

func (s *Server) markUnloaded(name string) {
	s.mu.Lock()
	delete(s.loaded, name)
	delete(s.lastUse, name)
	delete(s.active, name)
	delete(s.restarting, name)
	delete(s.attempts, name)
	delete(s.nextTry, name)
	loaded := len(s.loaded)
	s.mu.Unlock()
	s.Metrics.SetModels(loaded, s.residentCount())
}

func (s *Server) isResident(name string) bool {
	s.residentMu.Lock()
	defer s.residentMu.Unlock()
	return s.resident[name]
}

func (s *Server) residentCount() int {
	s.residentMu.Lock()
	defer s.residentMu.Unlock()
	return len(s.resident)
}

func (s *Server) residentNames() []string {
	s.residentMu.Lock()
	defer s.residentMu.Unlock()
	out := make([]string, 0, len(s.resident))
	for n := range s.resident {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Pin keeps a model loaded across idle scans and service restarts.
func (s *Server) Pin(name string) error {
	loaded := s.loadedCount()
	s.residentMu.Lock()
	if s.resident[name] {
		s.residentMu.Unlock()
		return nil
	}
	s.resident[name] = true
	count := len(s.resident)
	err := resident.Save(s.Conf.RunDir(), s.residentNamesLocked())
	if err != nil {
		delete(s.resident, name)
	}
	s.residentMu.Unlock()
	if err != nil {
		return err
	}
	s.Metrics.SetModels(loaded, count)
	logging.Info("resident.pin", "model", name)
	return nil
}

// Unpin removes a model from the resident set. It does not stop the engine.
func (s *Server) Unpin(name string) error {
	loaded := s.loadedCount()
	s.residentMu.Lock()
	if !s.resident[name] {
		s.residentMu.Unlock()
		return nil
	}
	delete(s.resident, name)
	count := len(s.resident)
	err := resident.Save(s.Conf.RunDir(), s.residentNamesLocked())
	if err != nil {
		s.resident[name] = true
	}
	s.residentMu.Unlock()
	if err != nil {
		return err
	}
	s.Metrics.SetModels(loaded, count)
	logging.Info("resident.unpin", "model", name)
	return nil
}

func (s *Server) residentNamesLocked() []string {
	out := make([]string, 0, len(s.resident))
	for n := range s.resident {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *Server) loadedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.loaded)
}

// startedAt parses the instance start time, falling back to now.
func startedAt(inst *engine.Instance) time.Time {
	if t, err := time.Parse(time.RFC3339, inst.Started); err == nil {
		return t
	}
	return time.Now()
}

func newUpstreamClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
			ForceAttemptHTTP2:   false,
		},
	}
}
