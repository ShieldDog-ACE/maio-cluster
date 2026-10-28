package cli

import (
	"fmt"
	"os"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/platform"
)

func (o *options) cmdService(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: maio service install|uninstall|status [--worker]")
		return 0
	}
	action := args[0]
	worker := false
	for _, a := range args[1:] {
		switch a {
		case "--worker":
			worker = true
		case "--coordinator":
			worker = false
		case "--help", "-h":
			fmt.Println("usage: maio service install|uninstall|status [--worker]")
			return 0
		}
	}
	switch action {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			errs.Print(os.Stderr, err)
			return 1
		}
		spec := platform.ServiceSpec{
			System: o.system, Coordinator: !worker, ConfigPath: o.conf.Path, BinaryPath: exe,
		}
		if err := o.adapter.ServiceInstall(spec); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		role := "coordinator + agent"
		if worker {
			role = "agent"
		}
		fmt.Printf("service installed (%s, %s scope) and started\n", role, scopeName(o.system))
		return 0
	case "uninstall", "remove":
		if err := o.adapter.ServiceUninstall(o.system); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println("service removed")
		return 0
	case "status":
		state, err := o.adapter.ServiceStatus(o.system)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println(state)
		return 0
	default:
		return usageMsg("usage: maio service install|uninstall|status [--worker]")
	}
}

func scopeName(system bool) string {
	if system {
		return "system"
	}
	return "user"
}

func printHelp() {
	fmt.Print(`maio v4.1 — distributed llama.cpp cluster

usage: maio [--system] [--config PATH] [--market URL] [--json] <command> [args]

  init [--token T]         create a cluster identity and configuration
  install <plugin>         install a plugin from the market
  pull <model>             download a model from the market
  plugins [list|rm|update] manage installed plugins
  market [--plugins|--models] [--refresh]
  search <query>           search plugins and models
  web install|status|remove  optional web console
  cluster up               start the coordinator and the local agent
  cluster join <code>      join a coordinator from another machine
  cluster join-code        print a pairing code
  cluster nodes            list members
  cluster plan <model>     preview layer placement
  cluster distribute <m>   copy a model to workers
  run <model> <prompt>     run inference across the cluster
  jobs                     list jobs
  job <id>                 show one job
  cancel <id>              cancel a job
  agent                    run a worker in the foreground
  model list|install|show|rm|path
  id | whoami              machine ID, node ID and registered token
  status                   local and cluster status
  doctor                   diagnose config, hardware, RPC, links
  link list|usb|test       inspect links and set up USB reverse tunnels
  service install|uninstall|status
  config path|show|keys
  logs [--tail N] [--path]
  token [show|rotate|set <token>]
  version

scopes:
  default                  per-user config and service
  --system                 machine-wide config and service (root/admin)

transports: LAN, Ethernet cable, USB tethering/data cable, hotspot.
`)
}
