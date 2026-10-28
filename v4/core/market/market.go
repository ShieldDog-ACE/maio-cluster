// Package market fetches the maio plugin and model market.
//
// The market is a plain GitHub repository: plugins.json and models.json are
// the only database. Clients read them over HTTPS (or a local mirror) and
// resolve names to GitHub repositories / GGUF URLs. Nothing is cached in the
// source tree.
package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

// DefaultBaseURL is the public market. Every component uses this unless
// MAIO_MARKET_URL or --market overrides it.
const DefaultBaseURL = "https://raw.githubusercontent.com/ShieldDog-ACE/maio-market/main"

const (
	maxIndexBytes = 4 << 20
	cacheTTL      = 30 * time.Second
)

var (
	pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	modelName  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	repoURL    = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	sha256Hex  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Plugin is one entry in plugins.json.
type Plugin struct {
	Name        string   `json:"name"`
	Author      string   `json:"author"`
	Repo        string   `json:"repo"`
	Description string   `json:"description"`
	Branch      string   `json:"branch,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Model is one entry in models.json.
type Model struct {
	Name        string `json:"name"`
	Author      string `json:"author"`
	URL         string `json:"url"`
	Quant       string `json:"quant,omitempty"`
	Context     int    `json:"context,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Description string `json:"description"`
}

type pluginIndex struct {
	Schema  int      `json:"schema"`
	Updated string   `json:"updated,omitempty"`
	Plugins []Plugin `json:"plugins"`
}

type modelIndex struct {
	Schema  int     `json:"schema"`
	Updated string  `json:"updated,omitempty"`
	Models  []Model `json:"models"`
}

// Client reads one market base URL. It is safe for concurrent use.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu        sync.Mutex
	plugins   []Plugin
	pluginsAt time.Time
	models    []Model
	modelsAt  time.Time
}

// New builds a client. An empty base URL means DefaultBaseURL.
func New(base string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		BaseURL: base,
		HTTP: &http.Client{
			Timeout:   30 * time.Second,
			Transport: newTransport(),
		},
	}
}

func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     false,
		// Prefer IPv4: some networks advertise AAAA but cannot route it,
		// which makes Go's happy-eyeballs path stall longer than curl.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
				return conn, nil
			}
			return dialer.DialContext(ctx, "tcp", addr)
		},
	}
}

// Refresh drops the short-lived cache.
func (c *Client) Refresh() {
	c.mu.Lock()
	c.plugins, c.pluginsAt = nil, time.Time{}
	c.models, c.modelsAt = nil, time.Time{}
	c.mu.Unlock()
}

// Plugins returns the validated plugin list.
func (c *Client) Plugins(ctx context.Context) ([]Plugin, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.plugins != nil && time.Since(c.pluginsAt) < cacheTTL {
		return append([]Plugin(nil), c.plugins...), nil
	}
	raw, err := c.fetch(ctx, "plugins.json")
	if err != nil {
		return nil, err
	}
	var idx pluginIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, errs.Wrap(errs.Config, err, "plugins.json is not valid JSON")
	}
	if idx.Schema != 1 {
		return nil, errs.New(errs.Config, "plugins.json: unsupported schema %d", idx.Schema)
	}
	seen := map[string]bool{}
	for i := range idx.Plugins {
		p := &idx.Plugins[i]
		p.Name = strings.ToLower(strings.TrimSpace(p.Name))
		if !pluginName.MatchString(p.Name) {
			return nil, errs.New(errs.Config, "plugins.json: invalid name %q", p.Name)
		}
		if seen[p.Name] {
			return nil, errs.New(errs.Config, "plugins.json: duplicate plugin %q", p.Name)
		}
		seen[p.Name] = true
		if strings.TrimSpace(p.Author) == "" {
			return nil, errs.New(errs.Config, "plugins.json: %s has no author", p.Name)
		}
		if !repoURL.MatchString(strings.TrimSuffix(p.Repo, "/")) {
			return nil, errs.New(errs.Config, "plugins.json: %s has an invalid repo %q", p.Name, p.Repo)
		}
		p.Repo = strings.TrimSuffix(p.Repo, "/")
		if p.Branch == "" {
			p.Branch = "main"
		}
	}
	c.plugins, c.pluginsAt = idx.Plugins, time.Now()
	return append([]Plugin(nil), c.plugins...), nil
}

// Models returns the validated model list.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models != nil && time.Since(c.modelsAt) < cacheTTL {
		return append([]Model(nil), c.models...), nil
	}
	raw, err := c.fetch(ctx, "models.json")
	if err != nil {
		return nil, err
	}
	var idx modelIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, errs.Wrap(errs.Config, err, "models.json is not valid JSON")
	}
	if idx.Schema != 1 {
		return nil, errs.New(errs.Config, "models.json: unsupported schema %d", idx.Schema)
	}
	seen := map[string]bool{}
	for i := range idx.Models {
		m := &idx.Models[i]
		m.Name = strings.ToLower(strings.TrimSpace(m.Name))
		if !modelName.MatchString(m.Name) {
			return nil, errs.New(errs.Config, "models.json: invalid name %q", m.Name)
		}
		if seen[m.Name] {
			return nil, errs.New(errs.Config, "models.json: duplicate model %q", m.Name)
		}
		seen[m.Name] = true
		u, err := url.Parse(m.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, errs.New(errs.Config, "models.json: %s has an invalid url %q", m.Name, m.URL)
		}
		if m.SHA256 != "" && !sha256Hex.MatchString(m.SHA256) {
			return nil, errs.New(errs.Config, "models.json: %s has an invalid sha256", m.Name)
		}
	}
	c.models, c.modelsAt = idx.Models, time.Now()
	return append([]Model(nil), c.models...), nil
}

// FindPlugin resolves one plugin by name.
func (c *Client) FindPlugin(ctx context.Context, name string) (Plugin, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	list, err := c.Plugins(ctx)
	if err != nil {
		return Plugin{}, err
	}
	for _, p := range list {
		if p.Name == name {
			return p, nil
		}
	}
	return Plugin{}, errs.New(errs.NotFound, "plugin %q is not in the market", name).
		WithHint("run \"maio market\" to list available plugins")
}

// FindModel resolves one model by name.
func (c *Client) FindModel(ctx context.Context, name string) (Model, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	list, err := c.Models(ctx)
	if err != nil {
		return Model{}, err
	}
	for _, m := range list {
		if m.Name == name {
			return m, nil
		}
	}
	return Model{}, errs.New(errs.NotFound, "model %q is not in the market", name).
		WithHint("run \"maio market --models\" to list available models")
}

func (c *Client) fetch(ctx context.Context, name string) ([]byte, error) {
	var last error
	for _, base := range c.endpoints() {
		data, err := c.fetchFrom(ctx, base, name)
		if err == nil {
			return data, nil
		}
		last = err
	}
	if last == nil {
		last = errs.New(errs.Unavailable, "market is unreachable")
	}
	return nil, last
}

func (c *Client) endpoints() []string {
	if c.BaseURL != DefaultBaseURL {
		return []string{c.BaseURL}
	}
	// api.github.com is the fastest path here and returns the raw file with
	// Accept: application/vnd.github.raw. raw.githubusercontent.com is the
	// canonical fallback; jsDelivr covers networks where both are blocked.
	return []string{
		"https://api.github.com/repos/ShieldDog-ACE/maio-market/contents",
		DefaultBaseURL,
		"https://cdn.jsdelivr.net/gh/ShieldDog-ACE/maio-market@main",
	}
}

func (c *Client) fetchFrom(ctx context.Context, base, name string) ([]byte, error) {
	if isLocal(base) {
		path := strings.TrimPrefix(base, "file://")
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return nil, errs.Wrap(errs.NotFound, err, "cannot read market file %s", name)
		}
		if len(data) > maxIndexBytes {
			return nil, errs.New(errs.Config, "market file %s is too large", name)
		}
		return data, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+name, nil)
	if err != nil {
		return nil, errs.Wrap(errs.Usage, err, "invalid market URL")
	}
	req.Header.Set("Accept", "application/vnd.github.raw, application/json")
	req.Header.Set("User-Agent", "maio-market/1")
	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		resp, lastErr = c.HTTP.Do(req)
		if lastErr == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			resp = nil
		}
	}
	if lastErr != nil {
		return nil, errs.Wrap(errs.Network, lastErr, "cannot reach market %s", base).
			WithHint("check the network, or pass --market / set MAIO_MARKET_URL")
	}
	if resp == nil {
		return nil, errs.New(errs.Unavailable, "market did not answer after retries: %s", base)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, errs.New(errs.Unavailable, "market returned HTTP %d for %s", resp.StatusCode, name)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
	if err != nil {
		return nil, errs.Wrap(errs.Network, err, "cannot read market response")
	}
	if len(data) > maxIndexBytes {
		return nil, errs.New(errs.Config, "market file %s is too large", name)
	}
	return data, nil
}

func isLocal(base string) bool {
	if strings.HasPrefix(base, "file://") || strings.HasPrefix(base, "/") || strings.HasPrefix(base, "./") || strings.HasPrefix(base, "../") {
		return true
	}
	return false
}

// FormatPlugin renders a compact one-line description.
func FormatPlugin(p Plugin) string {
	return fmt.Sprintf("%-24s %-16s %s", p.Name, p.Author, p.Description)
}
