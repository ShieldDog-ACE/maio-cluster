package server

import (
	"context"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

func (s *Server) restoreResident(ctx context.Context) {
	names := s.residentNames()
	if len(names) == 0 {
		return
	}
	logging.Info("resident.restore_start", "count", len(names))
	for _, name := range names {
		if ctx.Err() != nil || s.shutting.Load() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		if _, _, err := s.ensure(ctx, name); err != nil {
			logging.Error("resident.restore_failed", err, "model", name)
			continue
		}
		logging.Info("resident.restored", "model", name)
	}
}

func (s *Server) idleLoop(ctx context.Context, idle time.Duration) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reap(idle)
		}
	}
}

func (s *Server) reap(idle time.Duration) {
	if idle <= 0 {
		return
	}
	insts, err := s.Eng.List()
	if err != nil {
		logging.Warn("http.idle_scan_failed", "error", err.Error())
		return
	}
	now := time.Now()
	for _, in := range insts {
		s.mu.Lock()
		active := s.active[in.Name]
		loaded := s.loaded[in.Name]
		last := s.lastUse[in.Name]
		s.mu.Unlock()
		if active > 0 || !loaded || s.isResident(in.Name) {
			continue
		}
		if last.IsZero() {
			last = startedAt(&in)
		}
		if now.Sub(last) >= idle {
			logging.Info("engine.idle_unload", "model", in.Name, "idle_s", int(now.Sub(last).Seconds()))
			if err := s.Eng.Stop(in.Name); err != nil {
				logging.Warn("engine.idle_unload_failed", "model", in.Name, "error", err.Error())
				continue
			}
			s.markUnloaded(in.Name)
		}
	}
}

func (s *Server) superviseLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.supervise(ctx)
		}
	}
}

// supervise restarts resident or recently used engines that stopped answering.
// Restarts are serialized by engine.Manager, use exponential backoff, and give
// up proactively for unpinned models after several failures.
func (s *Server) supervise(ctx context.Context) {
	insts, err := s.Eng.List()
	if err != nil {
		logging.Warn("engine.supervise_list_failed", "error", err.Error())
		return
	}
	if n, err := s.Eng.CleanupStale(); err == nil && n > 0 {
		logging.Info("engine.cleanup_done", "removed", n)
	}
	loaded, active, _ := s.loadedState()
	present := make(map[string]bool, len(insts))
	now := time.Now()

	for _, in := range insts {
		name := in.Name
		present[name] = true
		if s.Eng.Health(in.Port) == nil {
			s.resetRestart(name)
			continue
		}
		if active[name] > 0 {
			continue
		}
		resident := s.isResident(name)
		if !resident && !loaded[name] {
			continue
		}
		if !resident && loaded[name] {
			// An unpinned model that crashed is loaded lazily on the next
			// request instead of being restarted forever.
			s.markUnloaded(name)
			continue
		}
		s.scheduleRestart(ctx, name, now)
	}

	// Resident models must exist even if their stale runtime file was removed.
	for _, name := range s.residentNames() {
		if !present[name] {
			s.scheduleRestart(ctx, name, now)
		}
	}
	// Loaded, unpinned models without runtime state are simply dropped.
	for name := range loaded {
		if !present[name] && !s.isResident(name) {
			s.markUnloaded(name)
		}
	}
}

func (s *Server) resetRestart(name string) {
	s.mu.Lock()
	delete(s.attempts, name)
	delete(s.nextTry, name)
	s.mu.Unlock()
}

func (s *Server) scheduleRestart(ctx context.Context, name string, now time.Time) {
	s.mu.Lock()
	if s.restarting[name] {
		s.mu.Unlock()
		return
	}
	if next, ok := s.nextTry[name]; ok && now.Before(next) {
		s.mu.Unlock()
		return
	}
	s.restarting[name] = true
	s.mu.Unlock()
	go s.restartOne(ctx, name)
}

func (s *Server) restartOne(ctx context.Context, name string) {
	defer func() {
		s.mu.Lock()
		delete(s.restarting, name)
		s.mu.Unlock()
	}()
	logging.Warn("engine.restart", "model", name)
	if err := s.Eng.Stop(name); err != nil {
		logging.Warn("engine.restart_stop_failed", "model", name, "error", err.Error())
	}
	_, _, err := s.ensure(ctx, name)
	s.mu.Lock()
	if err != nil {
		s.attempts[name]++
		backoff := time.Duration(1<<minInt(s.attempts[name], 6)) * time.Second
		if backoff > time.Minute {
			backoff = time.Minute
		}
		s.nextTry[name] = time.Now().Add(backoff)
		attempt := s.attempts[name]
		s.mu.Unlock()
		s.Metrics.IncEngineRestart(false)
		logging.Error("engine.restart_failed", err, "model", name, "attempt", attempt, "retry_in_s", int(backoff.Seconds()))
		return
	}
	delete(s.attempts, name)
	delete(s.nextTry, name)
	s.mu.Unlock()
	s.Metrics.IncEngineRestart(true)
	logging.Info("engine.restarted", "model", name)
}

func (s *Server) metricsLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Metrics.SetModels(s.loadedCount(), s.residentCount())
			s.Metrics.SetQueue(s.Queue.Depth(), s.Queue.MaxQueue())
		}
	}
}

// applyKeepAlive implements the Ollama keep_alive hint:
//   - nil or positive: keep the model for that long (negative = pin forever)
//   - zero: unload shortly after the response
func (s *Server) applyKeepAlive(name string, ka *time.Duration) {
	if ka == nil {
		s.touch(name)
		return
	}
	if *ka < 0 {
		if err := s.Pin(name); err != nil {
			logging.Warn("resident.pin_failed", "model", name, "error", err.Error())
		}
		return
	}
	if *ka == 0 {
		time.AfterFunc(200*time.Millisecond, func() { s.unloadAsync(name) })
		return
	}
	if s.Conf.IdleUnload <= 0 {
		return
	}
	// Expiry = lastUse + idle. Set lastUse so it lands at now + keep_alive.
	s.mu.Lock()
	s.lastUse[name] = time.Now().Add(*ka - time.Duration(s.Conf.IdleUnload)*time.Second)
	s.mu.Unlock()
}

func (s *Server) unloadAsync(name string) {
	s.mu.Lock()
	active := s.active[name]
	s.mu.Unlock()
	if active > 0 {
		// A later request reused the model after keep_alive=0 arrived.
		return
	}
	_ = s.Unpin(name)
	if err := s.Eng.Stop(name); err != nil {
		logging.Warn("engine.unload_failed", "model", name, "error", err.Error())
		return
	}
	s.markUnloaded(name)
	logging.Info("engine.keep_alive_expired", "model", name)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
