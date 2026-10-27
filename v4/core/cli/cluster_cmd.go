package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/agent"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/discovery"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/inference"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
)

func (o *options) cmdCluster(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: maio cluster up|join|join-code|nodes|status|plan|distribute|discover")
		return 0
	}
	switch args[0] {
	case "up":
		return o.clusterUp(args[1:])
	case "join":
		return o.clusterJoin(args[1:])
	case "join-code":
		return o.clusterJoinCode(args[1:])
	case "nodes":
		return o.clusterNodes(args[1:])
	case "status":
		return o.clusterStatus(args[1:])
	case "plan":
		return o.clusterPlan(args[1:])
	case "distribute":
		return o.clusterDistribute(args[1:])
	case "discover":
		return o.clusterDiscover(args[1:])
	case "leave":
		fmt.Println("this node has stopped participating; its entry expires after 10 minutes")
		return 0
	case "-h", "--help":
		fmt.Println("usage: maio cluster <command>")
		fmt.Println("  up [--no-agent]       start coordinator and local agent")
		fmt.Println("  join <code>           join a coordinator (writes config)")
		fmt.Println("  join-code             print the pairing code")
		fmt.Println("  nodes                 list members")
		fmt.Println("  status                cluster and local status")
		fmt.Println("  plan <model>          preview layer placement")
		fmt.Println("  distribute <model>    copy a model to workers")
		fmt.Println("  discover              listen for a coordinator beacon")
		return 0
	default:
		errs.Print(os.Stderr, errs.New(errs.Usage, "unknown cluster command %q", args[0]))
		return 2
	}
}

func (o *options) clusterUp(args []string) int {
	noAgent := false
	for _, a := range args {
		switch a {
		case "--no-agent":
			noAgent = true
		case "-h", "--help":
			fmt.Println("usage: maio cluster up [--no-agent]")
			return 0
		default:
			errs.Print(os.Stderr, errs.New(errs.Usage, "unknown argument %q", a))
			return 2
		}
	}
	if err := newIdentity(o.conf); err != nil {
		errs.Print(os.Stderr, err)
		return 1
	}
	if err := o.conf.Save(); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	store := o.store()
	coord, err := cluster.NewCoordinator(o.conf, o.log)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	coord.Models = store
	coord.SetRunner(inference.NewRunner(o.conf, store, coord, o.log))

	ctx, cancel := signalContext()
	defer cancel()
	errCh := make(chan error, 2)
	go func() { errCh <- coord.Start(ctx) }()
	if err := waitHealth(ctx, fmt.Sprintf("127.0.0.1:%d", o.conf.Cluster.Port), 10*time.Second); err != nil {
		errs.Print(os.Stderr, err)
		cancel()
		return errs.ExitCode(err)
	}
	info := coord.Info()
	port := info.Port
	advertise := o.advertiseHost()
	code, _ := cluster.EncodeJoinCode(cluster.JoinCode{
		ClusterID: info.ID, Name: info.Name, Host: advertise, Port: port, Token: o.conf.Cluster.Token,
	})
	fmt.Printf("coordinator %s listening on %s:%d\n", info.Name, o.conf.Cluster.Bind, port)
	fmt.Printf("join code: %s\n", code)

	if o.conf.Cluster.Discovery {
		b := discovery.Beacon{ClusterID: info.ID, Name: info.Name, Host: advertise, Port: port}
		go func() {
			if err := discovery.Broadcast(ctx, o.conf.Cluster.Token, b, discovery.Port(port)); err != nil && ctx.Err() == nil {
				o.log.Warn("discovery.broadcast_stopped", "err", err)
			}
		}()
	}

	if !noAgent {
		local := *o.conf
		local.Cluster.Seeds = []string{fmt.Sprintf("127.0.0.1:%d", port)}
		local.Cluster.Discovery = false
		a, err := agent.New(&local, o.log)
		if err != nil {
			errs.Print(os.Stderr, err)
			cancel()
			return errs.ExitCode(err)
		}
		a.MarkCoordinator()
		go func() { errCh <- a.Run(ctx) }()
		fmt.Println("local agent started")
	}
	fmt.Println("press Ctrl+C to stop")
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			errs.Print(os.Stderr, err)
			cancel()
			return errs.ExitCode(err)
		}
	}
	coord.Close()
	cancel()
	return 0
}

func (o *options) clusterJoin(args []string) int {
	if len(args) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio cluster join <join-code> [--start]"))
		return 2
	}
	codeText := args[0]
	start := false
	for _, a := range args[1:] {
		switch a {
		case "--start":
			start = true
		case "--help", "-h":
			fmt.Println("usage: maio cluster join <join-code> [--start]")
			return 0
		}
	}
	code, err := cluster.DecodeJoinCode(codeText)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	o.conf.Cluster.Token = code.Token
	o.conf.Cluster.ID = code.ClusterID
	if code.Name != "" {
		o.conf.Cluster.Name = code.Name
	}
	o.conf.Cluster.Seeds = []string{net.JoinHostPort(code.Host, strconv.Itoa(code.Port))}
	if err := o.conf.Save(); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("joined configuration written to %s\n", o.conf.Path)
	if start {
		a, err := agent.New(o.conf, o.log)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		ctx, cancel := signalContext()
		defer cancel()
		if err := a.Run(ctx); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		return 0
	}
	fmt.Println("next: maio service install   (or: maio agent)")
	return 0
}

func (o *options) clusterJoinCode(args []string) int {
	code, err := cluster.EncodeJoinCode(cluster.JoinCode{
		ClusterID: o.conf.Cluster.ID, Name: o.conf.Cluster.Name,
		Host: o.advertiseHost(), Port: o.conf.Cluster.Port, Token: o.conf.Cluster.Token,
	})
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Println(code)
	return 0
}

func (o *options) clusterNodes(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	nodes, err := o.client().Nodes(ctx)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		return o.printJSON(map[string]any{"nodes": nodes})
	}
	fmt.Printf("%-14s %-16s %-7s %-9s %-12s %-10s %-6s %s\n", "NAME", "ID", "ROLE", "OS", "ADDRESS", "STATE", "RPC", "GPU")
	for _, n := range nodes {
		fmt.Printf("%-14s %-16s %-7s %-9s %-12s %-10s %-6v %s\n",
			truncate(n.Name, 14), shortID(n.ID), n.Role, n.OS,
			nodeAddr(n), n.State, n.RPC.Available, truncate(n.Hardware.DeviceSummary(), 32))
	}
	return 0
}

func (o *options) clusterStatus(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	info, err := o.client().Info(ctx)
	if err != nil {
		if o.json {
			return o.printJSON(map[string]any{"error": err.Error()})
		}
		fmt.Printf("cluster: unreachable (%v)\n", err)
		state, _ := o.adapter.ServiceStatus(o.system)
		fmt.Printf("service: %s\n", state)
		fmt.Printf("local models: %s\n", o.conf.Agent.ModelsDir)
		return errs.ExitCode(err)
	}
	nodes, _ := o.client().Nodes(ctx)
	if o.json {
		return o.printJSON(map[string]any{"cluster": info, "nodes": nodes})
	}
	fmt.Printf("cluster: %s (%s), %d nodes, protocol %d\n", info.Name, info.ID, info.Nodes, info.Protocol)
	for _, n := range nodes {
		fmt.Printf("  %-16s %-8s %-10s %s\n", n.Name, n.Role, n.State, n.Hardware.DeviceSummary())
	}
	return 0
}

func (o *options) clusterPlan(args []string) int {
	if len(args) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio cluster plan <model> [--max-nodes N]"))
		return 2
	}
	modelName := args[0]
	maxNodes := 8
	for i := 1; i < len(args); i++ {
		if args[i] == "--max-nodes" && i+1 < len(args) {
			i++
			maxNodes, _ = strconv.Atoi(args[i])
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, err := o.client().Plan(ctx, modelName, maxNodes, "")
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		return o.printJSON(plan)
	}
	fmt.Printf("model %s: %d layers, context %d\n", plan.Model, plan.Layers, plan.Context)
	for _, p := range plan.Placements {
		fmt.Printf("  %-16s %6.1f%%  ~%d layers  %s\n", p.NodeName, 100*p.Weight/plan.TotalWeight, p.Layers, p.Host)
	}
	for _, w := range plan.Warnings {
		fmt.Println("warning: " + w)
	}
	return 0
}

func (o *options) clusterDistribute(args []string) int {
	if len(args) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio cluster distribute <model> [node-id...]"))
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	out, err := o.client().Distribute(ctx, args[0], args[1:])
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		code := o.printJSON(out)
		if code != 0 {
			return code
		}
	} else {
		fmt.Printf("distribution of %s finished\n", args[0])
	}
	failed := 0
	if results, ok := out["results"].([]any); ok {
		for _, raw := range results {
			if row, ok := raw.(map[string]any); ok {
				if okv, _ := row["ok"].(bool); !okv {
					failed++
					fmt.Fprintf(os.Stderr, "  %v: %v\n", row["node"], row["error"])
				}
			}
		}
	}
	if failed > 0 {
		return errs.ExitCode(errs.New(errs.Unavailable, "%d node(s) failed to receive the model", failed))
	}
	return 0
}

func (o *options) clusterDiscover(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := discovery.Find(ctx, o.conf.Cluster.Token, discovery.Port(o.conf.Cluster.Port), 6*time.Second)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("found %s (%s) at %s:%d\n", b.Name, b.ClusterID, b.Host, b.Port)
	return 0
}

func (o *options) cmdAgent(args []string) int {
	a, err := agent.New(o.conf, o.log)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	ctx, cancel := signalContext()
	defer cancel()
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	return 0
}

func (o *options) advertiseHost() string {
	if o.conf.Cluster.Advertise != "" {
		return o.conf.Cluster.Advertise
	}
	if ip := net.ParseIP(o.conf.Cluster.Bind); ip != nil && !ip.IsUnspecified() {
		return ip.String()
	}
	if a, ok := netinfo.BestAdvertise(); ok {
		return a.IP
	}
	return "127.0.0.1"
}

func waitHealth(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c := cluster.NewClient(addr, "")
		hctx, cancel := context.WithTimeout(ctx, time.Second)
		err := c.Health(hctx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errs.New(errs.Timeout, "coordinator did not answer health checks on %s", addr)
}

func nodeAddr(n cluster.Node) string {
	if n.APIPort > 0 {
		return net.JoinHostPort(n.Host, strconv.Itoa(n.APIPort))
	}
	return n.Host
}

func shortID(id string) string {
	if len(id) > 13 {
		return id[:13]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
