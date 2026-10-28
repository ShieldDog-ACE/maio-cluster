package cluster

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/market"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/plugin"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
	"github.com/ShieldDog-ACE/maio-cluster/v4/web"
)

const (
	webCookieName = "maio_web"
	webCookieTTL  = 7 * 24 * time.Hour
	csp           = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"
)

func marketBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("MAIO_MARKET_URL")); v != "" {
		return v
	}
	return market.DefaultBaseURL
}

func (c *Coordinator) handleWebIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if !webui.Installed(c.cfg.Agent.DataDir) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>maio</title>
<body style="font:15px system-ui;background:#0d1117;color:#e6edf3;padding:8vh 10vw">
<h1>maio v4.1</h1><p>Web 控制台还没有安装。</p>
<pre style="background:#171e27;padding:16px;border-radius:10px">maio web install</pre>
<p>CLI 与集群 API 不受影响。</p></body>`)
		return
	}
	http.ServeFile(w, r, filepath.Join(webui.Dir(c.cfg.Agent.DataDir), "index.html"))
}

func (c *Coordinator) handleWebAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !webui.Installed(c.cfg.Agent.DataDir) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(webui.Dir(c.cfg.Agent.DataDir), name))
	}
}

func (c *Coordinator) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	if !TokenEqual(req.Token, c.cfg.Cluster.Token) {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "cluster token is wrong", "")
		return
	}
	expires := time.Now().Add(webCookieTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     webCookieName,
		Value:    c.webCookieValue(expires),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(webCookieTTL.Seconds()),
		Expires:  expires,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_at": expires.UTC()})
}

func (c *Coordinator) handleWebLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: webCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1,
		Expires: time.Unix(1, 0),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Coordinator) handleWebMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cluster": c.Info()})
}

func (c *Coordinator) webCookieValue(expires time.Time) string {
	exp := expires.Unix()
	mac := hmac.New(sha256.New, []byte(c.cfg.Cluster.Token))
	fmt.Fprintf(mac, "maio-web-v1|%d", exp)
	return fmt.Sprintf("%d.%s", exp, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func (c *Coordinator) webCookieValid(r *http.Request) bool {
	ck, err := r.Cookie(webCookieName)
	if err != nil || ck.Value == "" {
		return false
	}
	parts := strings.SplitN(ck.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.cfg.Cluster.Token))
	fmt.Fprintf(mac, "maio-web-v1|%d", exp)
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(want))
}

func (c *Coordinator) handleMarketPlugins(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") != "" {
		c.Market.Refresh()
	}
	list, err := c.Market.Plugins(r.Context())
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": list, "base_url": c.Market.BaseURL})
}

func (c *Coordinator) handleMarketModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") != "" {
		c.Market.Refresh()
	}
	list, err := c.Market.Models(r.Context())
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": list, "base_url": c.Market.BaseURL})
}

func (c *Coordinator) handlePluginList(w http.ResponseWriter, r *http.Request) {
	list, problems := c.Plugins.List()
	msgs := make([]string, 0, len(problems))
	for _, p := range problems {
		msgs = append(msgs, p.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": list, "problems": msgs})
}

func (c *Coordinator) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var inst *plugin.Installed
	var err error
	if strings.TrimSpace(req.Repo) != "" {
		inst, err = c.Plugins.InstallURL(ctx, req.Repo, req.Name, false)
	} else {
		if strings.TrimSpace(req.Name) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "name or repo is required", "")
			return
		}
		var entry market.Plugin
		entry, err = c.Market.FindPlugin(ctx, req.Name)
		if err == nil {
			inst, err = c.Plugins.Install(ctx, entry, false)
		}
	}
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin": inst})
}

func (c *Coordinator) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	inst, err := c.Plugins.Update(ctx, name)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin": inst})
}

func (c *Coordinator) handlePluginRemove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := c.Plugins.Remove(name); err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Coordinator) localModels() *model.Store {
	if c.Models != nil {
		return c.Models
	}
	return model.NewStore(c.cfg.Agent.ModelsDir)
}

func (c *Coordinator) handleLocalModels(w http.ResponseWriter, r *http.Request) {
	list, problems := c.localModels().List()
	msgs := make([]string, 0, len(problems))
	for _, p := range problems {
		msgs = append(msgs, p.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": list, "problems": msgs})
}

func (c *Coordinator) handlePullModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Hour)
	defer cancel()
	name := strings.TrimSpace(req.Name)
	source := strings.TrimSpace(req.URL)
	wantSHA := strings.TrimSpace(req.SHA256)
	if source == "" {
		if name == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "name or url is required", "")
			return
		}
		m, err := c.Market.FindModel(ctx, name)
		if err != nil {
			writeErrCode(w, err)
			return
		}
		name, source, wantSHA = m.Name, m.URL, m.SHA256
	} else if name == "" {
		name = deriveModelName(source)
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "cannot derive a model name from the URL", "")
		return
	}
	e, err := c.localModels().Install(name, source, wantSHA, func(int64, int64) {})
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": e})
}

func deriveModelName(source string) string {
	if u, err := url.Parse(source); err == nil && u.Path != "" {
		source = u.Path
	}
	base := filepath.Base(source)
	base = strings.TrimSuffix(base, ".gguf")
	base = strings.ReplaceAll(strings.ToLower(base), "_", "-")
	if idx := strings.Index(base, "-q4"); idx > 0 {
		base = base[:idx]
	}
	if idx := strings.Index(base, "-q5"); idx > 0 {
		base = base[:idx]
	}
	if idx := strings.Index(base, "-q8"); idx > 0 {
		base = base[:idx]
	}
	return base
}

func (c *Coordinator) handleSystem(w http.ResponseWriter, r *http.Request) {
	hw := hardware.Detect(c.cfg.Agent.ModelsDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       version.Version,
		"os":            runtime.GOOS,
		"arch":          runtime.GOARCH,
		"data_dir":      c.cfg.Agent.DataDir,
		"models_dir":    c.cfg.Agent.ModelsDir,
		"plugins_dir":   c.Plugins.Dir,
		"web_dir":       webui.Dir(c.cfg.Agent.DataDir),
		"web_installed": webui.Installed(c.cfg.Agent.DataDir),
		"market_url":    c.Market.BaseURL,
		"hardware":      hw,
	})
}

func (c *Coordinator) handleJoinCode(w http.ResponseWriter, r *http.Request) {
	code, err := EncodeJoinCode(JoinCode{
		ClusterID: c.cfg.Cluster.ID, Name: c.cfg.Cluster.Name,
		Host: c.advertiseHost(), Port: c.Info().Port, Token: c.cfg.Cluster.Token,
	})
	if err != nil {
		writeErrCode(w, errs.Wrap(errs.Internal, err, "cannot encode join code"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code})
}
