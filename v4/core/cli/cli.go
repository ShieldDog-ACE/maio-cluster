// Package cli implements the cross-platform maio v4 command line.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/machineid"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/platform"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

type options struct {
	system     bool
	configPath string
	json       bool
	quiet      bool
	adapter    platform.Adapter
	conf       *config.Config
	paths      config.Paths
	log        *slog.Logger
	marketURL  string
}

// Run is the entry point. It returns a process exit code.
func Run(ad platform.Adapter, args []string) int {
	opts := &options{adapter: ad}
	rest, err := parseGlobal(opts, args)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if len(rest) == 0 {
		printHelp()
		return 0
	}
	cmd := rest[0]
	sub := rest[1:]

	switch cmd {
	case "-h", "--help", "help":
		printHelp()
		return 0
	case "version", "--version", "-v":
		fmt.Printf("maio %s (%s/%s, protocol %d)\n", version.Version, runtime.GOOS, runtime.GOARCH, version.Protocol)
		return 0
	case "init":
		return opts.cmdInit(sub)
	case "market":
		return opts.cmdMarket(sub)
	case "search":
		return opts.cmdSearch(sub)
	case "update":
		return opts.cmdUpdate(sub)
	}
	opts.paths = ad.Paths(opts.system)
	if opts.configPath == "" {
		opts.configPath = opts.paths.ConfigPath
	}
	if cmd == "cluster" && len(sub) > 0 && sub[0] == "join" {
		if _, statErr := os.Stat(opts.configPath); os.IsNotExist(statErr) {
			c := config.Default(opts.paths)
			c.Path = opts.configPath
			if err := newIdentity(c); err != nil {
				errs.Print(os.Stderr, err)
				return errs.ExitCode(err)
			}
			if err := c.Save(); err != nil {
				errs.Print(os.Stderr, err)
				return errs.ExitCode(err)
			}
		}
	}
	conf, err := config.Load(opts.configPath, opts.paths)
	if err != nil {
		if errs.CodeOf(err) == errs.NotFound && (cmd == "id" || cmd == "whoami" || cmd == "me") {
			// Identity must be printable before the machine has joined or
			// even initialized a cluster.
			conf = config.Default(opts.paths)
			conf.Path = opts.configPath
		} else {
			if errs.CodeOf(err) == errs.NotFound {
				errs.Print(os.Stderr, errs.New(errs.Config, "no configuration at %s", opts.configPath).
					WithHint("run \"maio init\" first, or pass --config"))
			} else {
				errs.Print(os.Stderr, err)
			}
			return errs.ExitCode(err)
		}
	}
	opts.conf = conf
	opts.log = newLogger(opts.quiet)

	switch cmd {
	case "cluster":
		return opts.cmdCluster(sub)
	case "agent":
		return opts.cmdAgent(sub)
	case "model":
		return opts.cmdModel(sub)
	case "run":
		return opts.cmdRun(sub)
	case "jobs":
		return opts.cmdJobs(sub)
	case "job":
		return opts.cmdJob(sub)
	case "cancel":
		return opts.cmdCancel(sub)
	case "id", "whoami", "me":
		return opts.cmdID(sub)
	case "status":
		return opts.cmdStatus(sub)
	case "doctor":
		return opts.cmdDoctor(sub)
	case "link":
		return opts.cmdLink(sub)
	case "service":
		return opts.cmdService(sub)
	case "config":
		return opts.cmdConfig(sub)
	case "logs":
		return opts.cmdLogs(sub)
	case "token":
		return opts.cmdToken(sub)
	case "web":
		return opts.cmdWeb(sub)
	case "install":
		return opts.cmdInstall(sub)
	case "pull":
		return opts.cmdPull(sub)
	case "plugins":
		return opts.cmdPlugins(sub)
	case "plugin":
		return opts.cmdPlugin(sub)
	default:
		err := errs.New(errs.Usage, "unknown command %q", cmd).
			WithHint(`run "maio --help" to see all commands`)
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
}

func parseGlobal(o *options, args []string) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--system":
			o.system = true
		case a == "--user":
			o.system = false
		case a == "--json":
			o.json = true
		case a == "--quiet" || a == "-q":
			o.quiet = true
		case a == "--config":
			if i+1 >= len(args) {
				return nil, errs.New(errs.Usage, "--config requires a path")
			}
			i++
			o.configPath = args[i]
		case strings.HasPrefix(a, "--config="):
			o.configPath = strings.TrimPrefix(a, "--config=")
		case a == "--market":
			if i+1 >= len(args) {
				return nil, errs.New(errs.Usage, "--market requires a URL or directory")
			}
			i++
			o.marketURL = args[i]
		case strings.HasPrefix(a, "--market="):
			o.marketURL = strings.TrimPrefix(a, "--market=")
		case a == "--help" || a == "-h":
			if len(rest) == 0 {
				return []string{"help"}, nil
			}
			rest = append(rest, a)
		default:
			rest = append(rest, args[i:]...)
			return rest, nil
		}
	}
	return rest, nil
}

func newLogger(quiet bool) *slog.Logger {
	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func (o *options) store() *model.Store {
	// The CLI downloads model files from arbitrary mirrors; never attach the
	// cluster token. Only the agent talks to the coordinator's file endpoint.
	return model.NewStore(o.conf.Agent.ModelsDir)
}

func (o *options) client() *cluster.Client {
	base := coordinatorURL(o.conf)
	return cluster.NewClient(base, o.conf.Cluster.Token)
}

func coordinatorURL(conf *config.Config) string {
	if len(conf.Cluster.Seeds) > 0 {
		return conf.Cluster.Seeds[0]
	}
	return fmt.Sprintf("127.0.0.1:%d", conf.Cluster.Port)
}

func newIdentity(conf *config.Config) error {
	if conf.Cluster.ID == "" {
		conf.Cluster.ID = cluster.NewID("mc_")
	}
	if conf.Cluster.Token == "" {
		tok, err := cluster.NewToken()
		if err != nil {
			return err
		}
		conf.Cluster.Token = tok
	}
	return nil
}

func (o *options) cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "cluster name")
	port := fs.Int("port", 47800, "coordinator API port")
	token := fs.String("token", "", "fixed long-lived cluster token")
	force := fs.Bool("force", false, "overwrite an existing configuration")
	noCompute := fs.Bool("no-compute", false, "coordinator does not accept shards")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	paths := o.adapter.Paths(o.system)
	if o.configPath == "" {
		o.configPath = paths.ConfigPath
	}
	o.paths = paths
	if _, err := os.Stat(o.configPath); err == nil && !*force {
		err := errs.New(errs.Conflict, "configuration already exists: %s", o.configPath).
			WithHint("edit it, or re-run with --force to generate a new cluster identity")
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	conf := config.Default(paths)
	conf.Path = o.configPath
	if *name != "" {
		conf.Cluster.Name = strings.TrimSpace(*name)
	}
	conf.Cluster.Port = *port
	conf.Cluster.Compute = !*noCompute
	if *token != "" {
		if err := cluster.ValidateToken(*token); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		conf.Cluster.Token = *token
	}
	if err := newIdentity(conf); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if err := os.MkdirAll(conf.Agent.DataDir, 0o700); err != nil {
		errs.Print(os.Stderr, errs.Wrap(errs.Permission, err, "cannot create %s", conf.Agent.DataDir))
		return 1
	}
	if err := os.MkdirAll(conf.Agent.ModelsDir, 0o755); err != nil {
		errs.Print(os.Stderr, errs.Wrap(errs.Permission, err, "cannot create %s", conf.Agent.ModelsDir))
		return 1
	}
	if err := conf.Save(); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	o.conf = conf
	fmt.Printf("initialized cluster %s (%s)\n", conf.Cluster.Name, conf.Cluster.ID)
	fmt.Printf("config: %s\n", conf.Path)
	fmt.Println("next: maio cluster up")
	return 0
}

func usageErr(err error) int {
	e := errs.New(errs.Usage, "%v", err).WithHint(`run "maio <command> --help"`)
	errs.Print(os.Stderr, e)
	return errs.ExitCode(e)
}

func (o *options) printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		errs.Print(os.Stderr, err)
		return 1
	}
	return 0
}

func (o *options) cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: maio config path|show|keys")
		return 0
	}
	switch args[0] {
	case "path":
		fmt.Println(o.conf.Path)
	case "show":
		fmt.Print(o.conf.Render())
	case "keys":
		fmt.Println("cluster.name, cluster.id, cluster.bind, cluster.port, cluster.advertise,")
		fmt.Println("cluster.compute, cluster.discovery, cluster.seeds, cluster.max_nodes,")
		fmt.Println("cluster.join_timeout_seconds, agent.name, agent.data_dir, agent.models_dir,")
		fmt.Println("agent.rpc_bin, agent.llama_bin, agent.rpc_port_base, agent.max_jobs,")
		fmt.Println("agent.heartbeat_seconds, agent.offline_seconds, security.tls_cert,")
		fmt.Println("security.tls_key, llama.server_bin, llama.ctx_size, llama.threads,")
		fmt.Println("llama.gpu_layers, llama.extra_args")
	default:
		errs.Print(os.Stderr, errs.New(errs.Usage, "unknown config action %q", args[0]))
		return 2
	}
	return 0
}

func (o *options) cmdToken(args []string) int {
	if len(args) == 0 || args[0] == "show" {
		fmt.Println(o.conf.Cluster.Token)
		return 0
	}
	switch args[0] {
	case "rotate":
		tok, err := cluster.NewToken()
		if err != nil {
			errs.Print(os.Stderr, err)
			return 1
		}
		o.conf.Cluster.Token = tok
		if err := o.conf.Save(); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println(tok)
		fmt.Println("token rotated; every worker must rejoin with the new join code")
		return 0
	case "set":
		if len(args) < 2 {
			errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio token set <token>").
				WithHint("generate one with: python3 -c 'import secrets; print(secrets.token_urlsafe(32))'"))
			return 2
		}
		if err := cluster.ValidateToken(args[1]); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		o.conf.Cluster.Token = args[1]
		if err := o.conf.Save(); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Printf("token set (%d characters); it does not expire\n", len(args[1]))
		fmt.Println("restart the coordinator and rejoin workers with the new join code")
		return 0
	}
	errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio token [show|rotate|set <token>]"))
	return 2
}

type localNodeState struct {
	ID          string `json:"id"`
	Coordinator string `json:"coordinator"`
}

func (o *options) cmdID(args []string) int {
	jsonOut := o.json
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "-h", "--help":
			fmt.Println("usage: maio id [--json]")
			fmt.Println("print this machine's ID, cluster node ID and registered token")
			return 0
		default:
			return usageMsg("unknown argument " + a)
		}
	}
	host, _ := os.Hostname()
	nodeID, coordinator := "", ""
	if raw, err := os.ReadFile(filepath.Join(o.conf.Agent.DataDir, "node.json")); err == nil {
		var st localNodeState
		if json.Unmarshal(raw, &st) == nil {
			nodeID, coordinator = st.ID, st.Coordinator
		}
	}
	machine := machineid.ID()
	token := o.conf.Cluster.Token
	if jsonOut {
		return o.printJSON(map[string]any{
			"machine_id":   machine,
			"hostname":     host,
			"node_id":      nodeID,
			"coordinator":  coordinator,
			"cluster_id":   o.conf.Cluster.ID,
			"cluster_name": o.conf.Cluster.Name,
			"token":        token,
			"config":       o.conf.Path,
		})
	}
	fmt.Printf("本机 ID:   %s\n", machine)
	fmt.Printf("主机名:    %s\n", host)
	if nodeID == "" {
		fmt.Println("节点 ID:   未加入集群（运行 \"maio agent\" 后生成）")
	} else {
		fmt.Printf("节点 ID:   %s\n", nodeID)
	}
	if o.conf.Cluster.ID != "" {
		fmt.Printf("集群:      %s (%s)\n", o.conf.Cluster.Name, o.conf.Cluster.ID)
	}
	if token == "" {
		fmt.Println("Token:     未注册")
	} else {
		fmt.Printf("Token:     %s\n", token)
	}
	fmt.Printf("配置文件:  %s\n", o.conf.Path)
	if coordinator != "" {
		fmt.Printf("协调者:    %s\n", coordinator)
	}
	return 0
}

func (o *options) cmdLogs(args []string) int {
	n := 100
	path := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tail":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &n)
			}
		case "--path":
			path = true
		}
	}
	dir := filepath.Join(o.conf.Agent.DataDir, "logs")
	if path {
		fmt.Println(dir)
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Println("no logs yet")
		return 0
	}
	var newest string
	for _, e := range entries {
		if !e.IsDir() {
			p := filepath.Join(dir, e.Name())
			if st, err := os.Stat(p); err == nil {
				if newest == "" || st.ModTime().After(modTime(newest)) {
					newest = p
				}
			}
		}
	}
	if newest == "" {
		fmt.Println("no logs yet")
		return 0
	}
	f, err := os.Open(newest)
	if err != nil {
		errs.Print(os.Stderr, err)
		return 1
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 16<<20))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	return 0
}

func modTime(path string) (t time.Time) {
	if st, err := os.Stat(path); err == nil {
		return st.ModTime()
	}
	return
}

// signalContext returns a context cancelled by SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		cancel()
		<-ch
		os.Exit(130)
	}()
	return ctx, cancel
}
