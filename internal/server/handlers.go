package server

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
)

type modelDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	QuantizationLevel string `json:"quantization_level,omitempty"`
	ContextLength     uint32 `json:"context_length,omitempty"`
	Vision            bool   `json:"vision,omitempty"`
}

type tagModel struct {
	Name       string       `json:"name"`
	Model      string       `json:"model"`
	ModifiedAt string       `json:"modified_at"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	Details    modelDetails `json:"details"`
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	entries, warns := registry.Scan(s.Conf.ModelsRoot)
	for _, warn := range warns {
		if errs.IsCode(warn, errs.Permission) || errs.IsCode(warn, errs.IO) {
			writeErr(w, r, statusFor(warn), warn)
			return
		}
		logging.Warn("registry.warning", "error", warn.Error())
	}
	models := make([]tagModel, 0, len(entries))
	for _, e := range entries {
		tm := tagModel{Name: e.Model.Name, Model: e.Model.Name, Size: e.Size}
		tm.Details.Format = e.Model.Format
		tm.Details.Family = "gguf"
		if st, err := os.Stat(e.Model.FilePath()); err == nil {
			tm.ModifiedAt = st.ModTime().UTC().Format(time.RFC3339Nano)
		}
		if info, err := gguf.Read(e.Model.FilePath()); err == nil {
			tm.Details.Family = info.Architecture
			tm.Details.QuantizationLevel = info.Quant
			tm.Details.ContextLength = info.ContextLength
			tm.Details.Vision = info.Vision
		}
		models = append(models, tm)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

type psModel struct {
	Name      string       `json:"name"`
	Model     string       `json:"model"`
	Size      int64        `json:"size"`
	SizeVRAM  uint64       `json:"size_vram"`
	Digest    string       `json:"digest"`
	ExpiresAt string       `json:"expires_at,omitempty"`
	Resident  bool         `json:"resident"`
	Status    string       `json:"status"`
	Port      int          `json:"port"`
	PID       int          `json:"pid"`
	Details   modelDetails `json:"details"`
}

func (s *Server) handlePs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	insts, err := s.Eng.List()
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	out := make([]psModel, 0, len(insts))
	for _, in := range insts {
		pm := psModel{
			Name: in.Name, Model: in.Name, SizeVRAM: in.VRAMBytes,
			Port: in.Port, PID: in.PID, Resident: s.isResident(in.Name),
		}
		if s.Eng.Health(in.Port) == nil {
			pm.Status = "ok"
		} else {
			pm.Status = "dead"
		}
		if md, ferr := registry.Find(s.Conf.ModelsRoot, in.Name); ferr == nil {
			if st, serr := os.Stat(md.FilePath()); serr == nil {
				pm.Size = st.Size()
			}
			if info, gerr := gguf.Read(md.FilePath()); gerr == nil {
				pm.Details = modelDetails{Format: md.Format, Family: info.Architecture,
					QuantizationLevel: info.Quant, ContextLength: info.ContextLength, Vision: info.Vision}
			}
		}
		if !pm.Resident && s.Conf.IdleUnload > 0 {
			last := s.lastUseFor(in.Name, startedAt(&in))
			pm.ExpiresAt = last.Add(time.Duration(s.Conf.IdleUnload) * time.Second).UTC().Format(time.RFC3339)
		}
		out = append(out, pm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		methodNotAllowed(w, r, "GET, POST")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		var req struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		}
		if r.Method == http.MethodPost {
			if err := s.decodeJSON(w, r, &req); err != nil {
				writeErr(w, r, statusFor(err), err)
				return
			}
		}
		name = req.Name
		if name == "" {
			name = req.Model
		}
	}
	if name == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model name").
			WithHint(`send {"name":"..."} or use /api/show?name=...`))
		return
	}
	md, err := registry.Find(s.Conf.ModelsRoot, name)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	fp := md.FilePath()
	st, statErr := os.Stat(fp)
	if statErr != nil {
		writeErr(w, r, http.StatusNotFound, errs.Newf(errs.NotFound, "model file is missing: %s", fp))
		return
	}
	info, ggufErr := gguf.Read(fp)
	details := modelDetails{Format: md.Format, Family: "gguf"}
	modelInfo := map[string]any{}
	caps := []string{"completion"}
	if ggufErr == nil {
		details = modelDetails{Format: md.Format, Family: info.Architecture,
			QuantizationLevel: info.Quant, ContextLength: info.ContextLength, Vision: info.Vision}
		modelInfo["general.architecture"] = info.Architecture
		modelInfo["context_length"] = info.ContextLength
		modelInfo["block_count"] = info.BlockCount
		modelInfo["embedding_length"] = info.EmbeddingLength
		if info.Vision {
			caps = append(caps, "vision")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":          md.Name,
		"model":         md.Name,
		"modified_at":   st.ModTime().UTC().Format(time.RFC3339Nano),
		"size":          st.Size(),
		"details":       details,
		"model_info":    modelInfo,
		"capabilities":  caps,
		"parameters":    "",
		"template":      "",
		"system":        "",
		"license":       "",
		"maio_config":   config.ModelConfigPath(md.Dir),
		"maio_gguf_err": errorString(ggufErr),
	})
}

type statsModel struct {
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Port       int       `json:"port"`
	PID        int       `json:"pid"`
	Context    uint32    `json:"context"`
	Parallel   int       `json:"parallel"`
	KVType     string    `json:"kv_type"`
	VRAMBytes  uint64    `json:"vram_bytes"`
	Started    string    `json:"started"`
	LastUsed   time.Time `json:"last_used"`
	Resident   bool      `json:"resident"`
	Active     int       `json:"active"`
	ExpiresAt  string    `json:"expires_at,omitempty"`
	LoadState  string    `json:"load_state"`
	RestartTry int       `json:"restart_attempts"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	snap := s.Metrics.Snapshot()
	insts, _ := s.Eng.List()
	s.mu.Lock()
	lastUse := make(map[string]time.Time, len(s.lastUse))
	for k, v := range s.lastUse {
		lastUse[k] = v
	}
	active := make(map[string]int, len(s.active))
	for k, v := range s.active {
		active[k] = v
	}
	loaded := make(map[string]bool, len(s.loaded))
	for k := range s.loaded {
		loaded[k] = true
	}
	attempts := make(map[string]int, len(s.attempts))
	for k, v := range s.attempts {
		attempts[k] = v
	}
	s.mu.Unlock()

	models := make([]statsModel, 0, len(insts))
	for _, in := range insts {
		state := "dead"
		if s.Eng.Health(in.Port) == nil {
			state = "ok"
		}
		loadState := "idle"
		if loaded[in.Name] {
			loadState = "loaded"
		}
		sm := statsModel{
			Name: in.Name, State: state, Port: in.Port, PID: in.PID,
			Context: in.Context, Parallel: in.Parallel, KVType: in.KVType,
			VRAMBytes: in.VRAMBytes, Started: in.Started,
			LastUsed: lastUse[in.Name], Resident: s.isResident(in.Name),
			Active: active[in.Name], LoadState: loadState,
			RestartTry: attempts[in.Name],
		}
		if sm.LastUsed.IsZero() {
			sm.LastUsed = startedAt(&in)
		}
		if !sm.Resident && s.Conf.IdleUnload > 0 {
			sm.ExpiresAt = sm.LastUsed.Add(time.Duration(s.Conf.IdleUnload) * time.Second).UTC().Format(time.RFC3339)
		}
		models = append(models, sm)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	info := s.Auth.Info()
	writeJSON(w, http.StatusOK, map[string]any{
		"metrics": snap,
		"service": map[string]any{
			"version":         s.Version,
			"uptime_seconds":  int64(s.Uptime().Seconds()),
			"host":            s.Conf.Host,
			"port":            s.Conf.Port,
			"pid":             os.Getpid(),
			"memory_limit_mb": s.Conf.MemoryLimitMB,
			"max_concurrency": s.Conf.MaxConcurrency,
			"max_queue":       s.Conf.MaxQueue,
			"parallel":        s.Conf.Parallel,
			"kv_type":         s.Conf.KVType,
			"idle_unload":     s.Conf.IdleUnload,
		},
		"auth": map[string]any{
			"required": info.Required,
			"source":   info.Source,
			"loopback": info.Loopback,
		},
		"models":   models,
		"resident": s.residentNames(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.Version})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	if s.shutting.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "stopping"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	name, err := s.requestModel(w, r)
	if err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if _, inst, err := s.ensure(r.Context(), name); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	} else if err := s.Pin(inst.Name); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "loaded", "model": name})
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		methodNotAllowed(w, r, "POST, DELETE")
		return
	}
	if r.URL.Query().Get("all") == "1" {
		s.unloadAll(w, r)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("model"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if r.Method == http.MethodPost {
		var req struct {
			All   bool   `json:"all"`
			Model string `json:"model"`
			Name  string `json:"name"`
		}
		if err := s.decodeJSON(w, r, &req); err != nil {
			writeErr(w, r, statusFor(err), err)
			return
		}
		if req.All {
			s.unloadAll(w, r)
			return
		}
		if req.Model != "" {
			name = req.Model
		} else if req.Name != "" {
			name = req.Name
		}
	}
	if name == "" {
		writeErr(w, r, http.StatusBadRequest, errs.New(errs.Usage, "missing model name").
			WithHint(`send {"model":"..."} or use ?model=...; use {"all":true} to unload everything`))
		return
	}
	s.unloadOne(w, r, name)
}

func (s *Server) unloadOne(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.Unpin(name); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	if err := s.Eng.Stop(name); err != nil {
		writeErr(w, r, statusFor(err), err)
		return
	}
	s.markUnloaded(name)
	writeJSON(w, http.StatusOK, map[string]any{"status": "unloaded", "model": name})
}

func (s *Server) unloadAll(w http.ResponseWriter, r *http.Request) {
	names := s.residentNames()
	if insts, err := s.Eng.List(); err == nil {
		for _, in := range insts {
			names = append(names, in.Name)
		}
	}
	seen := make(map[string]bool, len(names))
	unloaded := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		_ = s.Unpin(name)
		if err := s.Eng.Stop(name); err != nil {
			logging.Warn("engine.unload_all_failed", "model", name, "error", err.Error())
			continue
		}
		s.markUnloaded(name)
		unloaded = append(unloaded, name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "unloaded", "models": unloaded})
}

func (s *Server) requestModel(w http.ResponseWriter, r *http.Request) (string, error) {
	name := strings.TrimSpace(r.URL.Query().Get("model"))
	if name == "" {
		name = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if name != "" {
		return name, nil
	}
	var req struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	if err := s.decodeJSON(w, r, &req); err != nil {
		return "", err
	}
	if req.Model != "" {
		return req.Model, nil
	}
	if req.Name != "" {
		return req.Name, nil
	}
	return "", errs.New(errs.Usage, "missing model name").
		WithHint(`send {"model":"..."} or use ?model=...`)
}

func (s *Server) handleV1Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	entries, _ := registry.Scan(s.Conf.ModelsRoot)
	data := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		created := time.Now().Unix()
		if st, err := os.Stat(e.Model.FilePath()); err == nil {
			created = st.ModTime().Unix()
		}
		data = append(data, map[string]any{
			"id": e.Model.Name, "object": "model", "created": created, "owned_by": "maio",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) lastUseFor(name string, fallback time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.lastUse[name]; ok && !t.IsZero() {
		return t
	}
	return fallback
}

func (s *Server) loadedState() (loaded map[string]bool, active map[string]int, attempts map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded = make(map[string]bool, len(s.loaded))
	for k, v := range s.loaded {
		loaded[k] = v
	}
	active = make(map[string]int, len(s.active))
	for k, v := range s.active {
		active[k] = v
	}
	attempts = make(map[string]int, len(s.attempts))
	for k, v := range s.attempts {
		attempts[k] = v
	}
	return
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
