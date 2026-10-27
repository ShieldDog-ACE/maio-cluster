// Package config loads and saves the maio v4 cluster configuration.
//
// The file is a small TOML document. Every key is validated; unknown keys are
// rejected with a line number so a typo can never silently weaken the cluster
// token or the listen address.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/naming"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/toml"
)

// Cluster describes coordinator settings.
type Cluster struct {
	Name        string   `json:"name"`
	ID          string   `json:"id"`
	Token       string   `json:"token"`
	Bind        string   `json:"bind"`
	Port        int      `json:"port"`
	Advertise   string   `json:"advertise"`
	Compute     bool     `json:"compute"`
	Discovery   bool     `json:"discovery"`
	Seeds       []string `json:"seeds"`
	MaxNodes    int      `json:"max_nodes"`
	JoinTimeout int      `json:"join_timeout_seconds"`
}

// Agent describes the worker runtime.
type Agent struct {
	Name         string `json:"name"`
	DataDir      string `json:"data_dir"`
	ModelsDir    string `json:"models_dir"`
	RPCBin       string `json:"rpc_bin"`
	LlamaBin     string `json:"llama_bin"`
	RPCPortBase  int    `json:"rpc_port_base"`
	MaxJobs      int    `json:"max_jobs"`
	HeartbeatSec int    `json:"heartbeat_seconds"`
	OfflineSec   int    `json:"offline_seconds"`
}

// Security holds optional TLS material.
type Security struct {
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`
}

// Llama holds shared inference defaults.
type Llama struct {
	ServerBin string   `json:"server_bin"`
	Context   int      `json:"ctx_size"`
	Threads   int      `json:"threads"`
	GPULayers int      `json:"gpu_layers"`
	ExtraArgs []string `json:"extra_args"`
}

// Config is a complete maio v4 configuration.
type Config struct {
	Path     string   `json:"path"`
	Cluster  Cluster  `json:"cluster"`
	Agent    Agent    `json:"agent"`
	Security Security `json:"security"`
	Llama    Llama    `json:"llama"`
}

// Paths are the platform-specific default locations.
type Paths struct {
	ConfigPath string
	DataDir    string
	ModelsDir  string
	StateDir   string
	LogDir     string
}

// Default builds a configuration using platform paths and a generated identity.
func Default(paths Paths) *Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "maio-node"
	}
	host = sanitizeHost(host)
	return &Config{
		Path: paths.ConfigPath,
		Cluster: Cluster{
			Name:        "maio",
			Bind:        "0.0.0.0",
			Port:        47800,
			Compute:     true,
			Discovery:   true,
			MaxNodes:    64,
			JoinTimeout: 15,
		},
		Agent: Agent{
			Name:         host,
			DataDir:      paths.DataDir,
			ModelsDir:    paths.ModelsDir,
			RPCPortBase:  50100,
			MaxJobs:      2,
			HeartbeatSec: 3,
			OfflineSec:   15,
		},
		Llama: Llama{
			GPULayers: 99,
		},
	}
}

func sanitizeHost(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		case r == '_', r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "maio-node"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// Load reads path, applies defaults for omitted keys, and validates.
func Load(path string, paths Paths) (*Config, error) {
	cfg := Default(paths)
	cfg.Path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.NotFound, err, "configuration not found: %s", path).
				WithHint("run \"maio init\" to create a cluster configuration")
		}
		return nil, errs.Wrap(errs.Permission, err, "cannot read configuration %s", path)
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid configuration %s", path)
	}
	known := map[string]bool{}
	readString := func(key string, dst *string) error {
		known[key] = true
		if !doc.Has(key) {
			return nil
		}
		v, ok := doc.String(key)
		if !ok {
			return typeErr(path, doc, key, "string")
		}
		*dst = v
		return nil
	}
	readInt := func(key string, dst *int) error {
		known[key] = true
		if !doc.Has(key) {
			return nil
		}
		v, ok := doc.Int(key)
		if !ok {
			return typeErr(path, doc, key, "integer")
		}
		*dst = int(v)
		return nil
	}
	readBool := func(key string, dst *bool) error {
		known[key] = true
		if !doc.Has(key) {
			return nil
		}
		v, ok := doc.Bool(key)
		if !ok {
			return typeErr(path, doc, key, "boolean")
		}
		*dst = v
		return nil
	}
	readStrings := func(key string, dst *[]string) error {
		known[key] = true
		if !doc.Has(key) {
			return nil
		}
		v, ok := doc.Strings(key)
		if !ok {
			return typeErr(path, doc, key, "string array")
		}
		*dst = v
		return nil
	}
	reads := []func() error{
		func() error { return readString("cluster.name", &cfg.Cluster.Name) },
		func() error { return readString("cluster.id", &cfg.Cluster.ID) },
		func() error { return readString("cluster.token", &cfg.Cluster.Token) },
		func() error { return readString("cluster.bind", &cfg.Cluster.Bind) },
		func() error { return readInt("cluster.port", &cfg.Cluster.Port) },
		func() error { return readString("cluster.advertise", &cfg.Cluster.Advertise) },
		func() error { return readBool("cluster.compute", &cfg.Cluster.Compute) },
		func() error { return readBool("cluster.discovery", &cfg.Cluster.Discovery) },
		func() error { return readStrings("cluster.seeds", &cfg.Cluster.Seeds) },
		func() error { return readInt("cluster.max_nodes", &cfg.Cluster.MaxNodes) },
		func() error { return readInt("cluster.join_timeout_seconds", &cfg.Cluster.JoinTimeout) },
		func() error { return readString("agent.name", &cfg.Agent.Name) },
		func() error { return readString("agent.data_dir", &cfg.Agent.DataDir) },
		func() error { return readString("agent.models_dir", &cfg.Agent.ModelsDir) },
		func() error { return readString("agent.rpc_bin", &cfg.Agent.RPCBin) },
		func() error { return readString("agent.llama_bin", &cfg.Agent.LlamaBin) },
		func() error { return readInt("agent.rpc_port_base", &cfg.Agent.RPCPortBase) },
		func() error { return readInt("agent.max_jobs", &cfg.Agent.MaxJobs) },
		func() error { return readInt("agent.heartbeat_seconds", &cfg.Agent.HeartbeatSec) },
		func() error { return readInt("agent.offline_seconds", &cfg.Agent.OfflineSec) },
		func() error { return readString("security.tls_cert", &cfg.Security.TLSCert) },
		func() error { return readString("security.tls_key", &cfg.Security.TLSKey) },
		func() error { return readString("llama.server_bin", &cfg.Llama.ServerBin) },
		func() error { return readInt("llama.ctx_size", &cfg.Llama.Context) },
		func() error { return readInt("llama.threads", &cfg.Llama.Threads) },
		func() error { return readInt("llama.gpu_layers", &cfg.Llama.GPULayers) },
		func() error { return readStrings("llama.extra_args", &cfg.Llama.ExtraArgs) },
	}
	for _, r := range reads {
		if err := r(); err != nil {
			return nil, err
		}
	}
	for _, key := range doc.Keys() {
		if !known[key] {
			line := doc.Line(key)
			return nil, errs.New(errs.Config, "%s:%d: unknown key %q", path, line, key).
				WithHint("run \"maio config keys\" to list supported keys")
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func typeErr(path string, doc *toml.Document, key, want string) error {
	return errs.New(errs.Config, "%s:%d: %q must be a %s, got %s",
		path, doc.Line(key), key, want, doc.TypeName(key)).
		WithHint("fix the value or remove the key to use the default")
}

// Validate checks invariants that would otherwise fail at runtime.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Cluster.Name) == "" {
		return errs.New(errs.Config, "cluster.name must not be empty")
	}
	if c.Cluster.Port < 1 || c.Cluster.Port > 65535 {
		return errs.New(errs.Config, "cluster.port must be 1..65535, got %d", c.Cluster.Port)
	}
	if c.Cluster.Token != "" {
		if len(c.Cluster.Token) < 32 || len(c.Cluster.Token) > 128 {
			return errs.New(errs.Config, "cluster.token must be 32..128 characters, got %d", len(c.Cluster.Token)).
				WithHint("use URL-safe base64 (A-Z a-z 0-9 - _)")
		}
		for _, r := range c.Cluster.Token {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return errs.New(errs.Config, "cluster.token contains an unsupported character %q", r).
					WithHint("use only A-Z a-z 0-9 - _")
			}
		}
	}
	if c.Cluster.MaxNodes < 1 || c.Cluster.MaxNodes > 512 {
		return errs.New(errs.Config, "cluster.max_nodes must be 1..512")
	}
	if c.Cluster.JoinTimeout < 1 || c.Cluster.JoinTimeout > 300 {
		return errs.New(errs.Config, "cluster.join_timeout_seconds must be 1..300")
	}
	if c.Agent.Name == "" {
		return errs.New(errs.Config, "agent.name must not be empty")
	}
	if c.Agent.RPCPortBase < 1024 || c.Agent.RPCPortBase > 65000 {
		return errs.New(errs.Config, "agent.rpc_port_base must be 1024..65000")
	}
	if c.Agent.MaxJobs < 1 || c.Agent.MaxJobs > 64 {
		return errs.New(errs.Config, "agent.max_jobs must be 1..64")
	}
	if c.Agent.HeartbeatSec < 1 || c.Agent.HeartbeatSec > 60 {
		return errs.New(errs.Config, "agent.heartbeat_seconds must be 1..60")
	}
	if c.Agent.OfflineSec <= c.Agent.HeartbeatSec {
		return errs.New(errs.Config, "agent.offline_seconds must be greater than heartbeat_seconds")
	}
	if c.Agent.DataDir != "" && !filepath.IsAbs(c.Agent.DataDir) {
		return errs.New(errs.Config, "agent.data_dir must be absolute, got %q", c.Agent.DataDir)
	}
	if c.Agent.ModelsDir != "" && !filepath.IsAbs(c.Agent.ModelsDir) {
		return errs.New(errs.Config, "agent.models_dir must be absolute, got %q", c.Agent.ModelsDir)
	}
	if (c.Security.TLSCert == "") != (c.Security.TLSKey == "") {
		return errs.New(errs.Config, "security.tls_cert and security.tls_key must be set together")
	}
	if c.Llama.Context < 0 || c.Llama.Context > 1<<20 {
		return errs.New(errs.Config, "llama.ctx_size must be 0..1048576")
	}
	if c.Llama.Threads < 0 || c.Llama.Threads > 1024 {
		return errs.New(errs.Config, "llama.threads must be 0..1024")
	}
	return nil
}

// Save writes the configuration atomically.
func (c *Config) Save() error {
	if c.Path == "" {
		return errs.New(errs.Internal, "configuration path is empty")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create configuration directory")
	}
	tmp := c.Path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write configuration")
	}
	if _, err := f.WriteString(c.Render()); err != nil {
		f.Close()
		os.Remove(tmp)
		return errs.Wrap(errs.Internal, err, "cannot write configuration")
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return errs.Wrap(errs.Internal, err, "cannot close configuration")
	}
	if err := os.Rename(tmp, c.Path); err != nil {
		os.Remove(tmp)
		return errs.Wrap(errs.Internal, err, "cannot replace configuration")
	}
	return nil
}

// Render returns the TOML text.
func (c *Config) Render() string {
	var b strings.Builder
	b.WriteString("# maio v4 cluster configuration\n")
	b.WriteString("# Generated by maio. Keep this file private: cluster.token authorizes joins.\n\n")
	b.WriteString("[cluster]\n")
	q := func(k, v string) { fmt.Fprintf(&b, "%s = %s\n", k, strconv.Quote(v)) }
	q("name", c.Cluster.Name)
	q("id", c.Cluster.ID)
	q("token", c.Cluster.Token)
	q("bind", c.Cluster.Bind)
	fmt.Fprintf(&b, "port = %d\n", c.Cluster.Port)
	q("advertise", c.Cluster.Advertise)
	fmt.Fprintf(&b, "compute = %s\n", boolStr(c.Cluster.Compute))
	fmt.Fprintf(&b, "discovery = %s\n", boolStr(c.Cluster.Discovery))
	fmt.Fprintf(&b, "seeds = %s\n", quoteArray(c.Cluster.Seeds))
	fmt.Fprintf(&b, "max_nodes = %d\n", c.Cluster.MaxNodes)
	fmt.Fprintf(&b, "join_timeout_seconds = %d\n\n", c.Cluster.JoinTimeout)
	b.WriteString("[agent]\n")
	q("name", c.Agent.Name)
	q("data_dir", c.Agent.DataDir)
	q("models_dir", c.Agent.ModelsDir)
	q("rpc_bin", c.Agent.RPCBin)
	q("llama_bin", c.Agent.LlamaBin)
	fmt.Fprintf(&b, "rpc_port_base = %d\n", c.Agent.RPCPortBase)
	fmt.Fprintf(&b, "max_jobs = %d\n", c.Agent.MaxJobs)
	fmt.Fprintf(&b, "heartbeat_seconds = %d\n", c.Agent.HeartbeatSec)
	fmt.Fprintf(&b, "offline_seconds = %d\n\n", c.Agent.OfflineSec)
	b.WriteString("[security]\n")
	q("tls_cert", c.Security.TLSCert)
	q("tls_key", c.Security.TLSKey)
	b.WriteString("\n[llama]\n")
	q("server_bin", c.Llama.ServerBin)
	fmt.Fprintf(&b, "ctx_size = %d\n", c.Llama.Context)
	fmt.Fprintf(&b, "threads = %d\n", c.Llama.Threads)
	fmt.Fprintf(&b, "gpu_layers = %d\n", c.Llama.GPULayers)
	fmt.Fprintf(&b, "extra_args = %s\n", quoteArray(c.Llama.ExtraArgs))
	return b.String()
}

func quoteArray(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	parts := make([]string, len(v))
	for i, s := range v {
		parts[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// ModelName validates a model name from a config-adjacent context.
func ModelName(raw string) (string, error) { return naming.Normalize(raw) }

// Runtime describes the current platform for diagnostics.
func Runtime() string { return runtime.GOOS + "/" + runtime.GOARCH }
