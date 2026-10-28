package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

const maxBody = 4 << 20

func (c *Coordinator) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "version": c.info.Version, "protocol": c.info.Protocol, "time": time.Now().UTC(),
		})
	})
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.Info())
	})
	mux.HandleFunc("POST /v1/join", c.auth(c.handleJoin))
	mux.HandleFunc("POST /v1/heartbeat", c.auth(c.handleHeartbeat))
	mux.HandleFunc("GET /v1/nodes", c.auth(c.handleNodes))
	mux.HandleFunc("POST /v1/nodes/{id}/drain", c.auth(c.handleDrain))
	mux.HandleFunc("POST /v1/agent/commands/next", c.auth(c.handleNextCommand))
	mux.HandleFunc("POST /v1/agent/commands/result", c.auth(c.handleCommandResult))
	mux.HandleFunc("GET /v1/models", c.auth(c.handleModels))
	mux.HandleFunc("GET /v1/models/{name}/file", c.auth(c.handleModelFile))
	mux.HandleFunc("POST /v1/models/distribute", c.auth(c.handleDistribute))
	mux.HandleFunc("GET /v1/plan", c.auth(c.handlePlan))
	mux.HandleFunc("POST /v1/plan", c.auth(c.handlePlan))
	mux.HandleFunc("POST /v1/jobs", c.auth(c.handleCreateJob))
	mux.HandleFunc("GET /v1/jobs", c.auth(c.handleListJobs))
	mux.HandleFunc("GET /v1/jobs/{id}", c.auth(c.handleGetJob))
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", c.auth(c.handleCancelJob))
	mux.HandleFunc("GET /v1/jobs/{id}/events", c.auth(c.handleJobEvents))

	// Web console and package manager (v4.1).
	mux.HandleFunc("GET /", c.handleWebIndex)
	mux.HandleFunc("GET /app.js", c.handleWebAsset("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /style.css", c.handleWebAsset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/web/login", c.handleWebLogin)
	mux.HandleFunc("POST /v1/web/logout", c.auth(c.handleWebLogout))
	mux.HandleFunc("GET /v1/web/me", c.auth(c.handleWebMe))
	mux.HandleFunc("GET /v1/market/plugins", c.auth(c.handleMarketPlugins))
	mux.HandleFunc("GET /v1/market/models", c.auth(c.handleMarketModels))
	mux.HandleFunc("GET /v1/plugins", c.auth(c.handlePluginList))
	mux.HandleFunc("POST /v1/plugins/install", c.auth(c.handlePluginInstall))
	mux.HandleFunc("POST /v1/plugins/{name}/update", c.auth(c.handlePluginUpdate))
	mux.HandleFunc("DELETE /v1/plugins/{name}", c.auth(c.handlePluginRemove))
	mux.HandleFunc("GET /v1/models/local", c.auth(c.handleLocalModels))
	mux.HandleFunc("POST /v1/models/pull", c.auth(c.handlePullModel))
	mux.HandleFunc("GET /v1/system", c.auth(c.handleSystem))
	mux.HandleFunc("GET /v1/cluster/join-code", c.auth(c.handleJoinCode))
	return withCommon(mux)
}

func withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Maio-Protocol", fmt.Sprint(version.Protocol))
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (c *Coordinator) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !c.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "cluster token is missing or wrong",
				"paste the current code from \"maio cluster join-code\" on the coordinator")
			return
		}
		next(w, r)
	}
}

// authorized accepts an agent bearer token or the web console session cookie.
func (c *Coordinator) authorized(r *http.Request) bool {
	if TokenEqual(bearer(r), c.cfg.Cluster.Token) {
		return true
	}
	return c.webCookieValid(r)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func (c *Coordinator) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	resp, err := c.join(req, host)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (c *Coordinator) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb Heartbeat
	if err := decodeJSON(r, &hb); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	if err := c.heartbeat(hb); err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "now": time.Now().UTC()})
}

func (c *Coordinator) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"nodes": c.Nodes()})
}

func (c *Coordinator) handleDrain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !c.setDraining(id) {
		writeErr(w, http.StatusNotFound, "not_found", "node not found", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Coordinator) setDraining(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[id]
	if !ok {
		return false
	}
	m.node.State = Draining
	return true
}

func (c *Coordinator) handleNextCommand(w http.ResponseWriter, r *http.Request) {
	nodeID := r.Header.Get("X-Maio-Node")
	if nodeID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "X-Maio-Node header is required", "")
		return
	}
	cmd, err := c.nextCommand(r.Context(), nodeID)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	if cmd == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

func (c *Coordinator) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	nodeID := r.Header.Get("X-Maio-Node")
	if nodeID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "X-Maio-Node header is required", "")
		return
	}
	var res CommandResult
	if err := decodeJSON(r, &res); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	if !c.deliverResult(nodeID, res) {
		writeErr(w, http.StatusConflict, "unknown_command", "command is unknown or already completed", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Coordinator) handleModels(w http.ResponseWriter, r *http.Request) {
	type modelView struct {
		Name   string   `json:"name"`
		Size   int64    `json:"size_bytes"`
		Layers int      `json:"layers"`
		Quant  string   `json:"quant,omitempty"`
		Nodes  []string `json:"nodes"`
	}
	byName := map[string]*modelView{}
	c.mu.RLock()
	for _, m := range c.members {
		for _, ref := range m.node.Models {
			v := byName[ref.Name]
			if v == nil {
				v = &modelView{Name: ref.Name, Size: ref.Size, Layers: ref.Layers, Quant: ref.Quant}
				byName[ref.Name] = v
			}
			v.Nodes = append(v.Nodes, m.node.Name)
		}
	}
	c.mu.RUnlock()
	out := make([]modelView, 0, len(byName))
	for _, v := range byName {
		out = append(out, *v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (c *Coordinator) handleModelFile(w http.ResponseWriter, r *http.Request) {
	if c.Models == nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "model store is not configured", "")
		return
	}
	name := r.PathValue("name")
	e, err := c.Models.Get(name)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	http.ServeFile(w, r, e.AbsPath)
}

func (c *Coordinator) handleDistribute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string   `json:"name"`
		Nodes []string `json:"nodes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	if c.Models == nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "model store is not configured", "")
		return
	}
	e, err := c.Models.Get(req.Name)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	url := c.modelURL(e.Name)
	targets := req.Nodes
	if len(targets) == 0 {
		for _, n := range c.Nodes() {
			if n.State == Online && n.ID != c.CoordinatorNodeID() && !n.HasModel(e.Name) {
				targets = append(targets, n.ID)
			}
		}
	}
	type outcome struct {
		Node  string `json:"node"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make([]outcome, 0, len(targets))
	for _, id := range targets {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		_, err := c.Call(ctx, id, "model.prepare", map[string]string{
			"name": e.Name, "url": url, "sha256": "",
		}, 10*time.Minute)
		cancel()
		o := outcome{Node: id, OK: err == nil}
		if err != nil {
			o.Error = err.Error()
		}
		results = append(results, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": e.Name, "results": results})
}

// CoordinatorNodeID returns the node ID the local agent registered, if any.
func (c *Coordinator) CoordinatorNodeID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for id, m := range c.members {
		if m.node.Role == "coordinator" {
			return id
		}
	}
	return ""
}

func (c *Coordinator) modelURL(name string) string {
	host := c.advertiseHost()
	port := c.Info().Port
	return fmt.Sprintf("http://%s/v1/models/%s/file", net.JoinHostPort(host, strconv.Itoa(port)), name)
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSON(w, status, APIError{Code: status, Error: msg, Hint: hint})
}

func writeErrCode(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch errs.CodeOf(err) {
	case errs.Usage, errs.Config:
		status = http.StatusBadRequest
	case errs.NotFound:
		status = http.StatusNotFound
	case errs.Conflict:
		status = http.StatusConflict
	case errs.Auth:
		status = http.StatusUnauthorized
	case errs.Unavailable:
		status = http.StatusServiceUnavailable
	case errs.Timeout:
		status = http.StatusGatewayTimeout
	case errs.Canceled:
		status = 499
	case errs.Hardware:
		status = http.StatusUnprocessableEntity
	}
	var e *errs.Error
	hint := ""
	if errors.As(err, &e) && len(e.Hints) > 0 {
		hint = e.Hints[0]
	}
	writeErr(w, status, strings.ToLower(strings.ReplaceAll(errs.CodeOf(err).String(), "MAIO-", "")), err.Error(), hint)
}
