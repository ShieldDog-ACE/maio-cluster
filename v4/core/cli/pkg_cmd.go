package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/market"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/plugin"
	"github.com/ShieldDog-ACE/maio-cluster/v4/web"
)

func (o *options) marketClient() *market.Client {
	base := strings.TrimSpace(o.marketURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("MAIO_MARKET_URL"))
	}
	return market.New(base)
}

func (o *options) pluginManager() *plugin.Manager {
	return plugin.NewManager(o.conf.Agent.DataDir)
}

// cmdInstall installs a plugin from the market or a GitHub URL.
func (o *options) cmdInstall(args []string) int {
	force := false
	repo := ""
	name := ""
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force", "-f":
			force = true
		case "--repo":
			if i+1 >= len(args) {
				return usageMsg("--repo requires a GitHub URL")
			}
			i++
			repo = args[i]
		case "--name":
			if i+1 >= len(args) {
				return usageMsg("--name requires a value")
			}
			i++
			name = args[i]
		case "--help", "-h":
			fmt.Println("usage: maio install <plugin> [--force] [--repo https://github.com/o/r] [--name NAME]")
			fmt.Println("       plugins are listed in the market (see \"maio market\")")
			return 0
		default:
			if strings.HasPrefix(args[i], "-") {
				return usageMsg("unknown argument " + args[i])
			}
			pos = append(pos, args[i])
		}
	}
	if repo == "" {
		if len(pos) == 0 {
			return usageMsg("usage: maio install <plugin>")
		}
		name = pos[0]
	} else if name == "" && len(pos) > 0 {
		name = pos[0]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	mgr := o.pluginManager()
	var inst *plugin.Installed
	var err error
	if repo != "" {
		inst, err = mgr.InstallURL(ctx, repo, name, force)
	} else {
		entry, findErr := o.marketClient().FindPlugin(ctx, name)
		if findErr != nil {
			errs.Print(os.Stderr, findErr)
			return errs.ExitCode(findErr)
		}
		inst, err = mgr.Install(ctx, entry, force)
	}
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("installed plugin %s %s\n", inst.Name, inst.Version)
	fmt.Printf("  repo: %s\n", inst.Repo)
	fmt.Printf("  dir:  %s\n", inst.Dir)
	return 0
}

// cmdPull downloads a model from the market (or a direct URL/path).
func (o *options) cmdPull(args []string) int {
	var rawURL, sha string
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 >= len(args) {
				return usageMsg("--url requires a path or URL")
			}
			i++
			rawURL = args[i]
		case "--sha256":
			if i+1 >= len(args) {
				return usageMsg("--sha256 requires a value")
			}
			i++
			sha = args[i]
		case "--help", "-h":
			fmt.Println("usage: maio pull <model> [--url <url-or-path>] [--sha256 HEX]")
			fmt.Println("       models are listed in the market (see \"maio market --models\")")
			return 0
		default:
			if strings.HasPrefix(args[i], "--") {
				return usageMsg("unknown argument " + args[i])
			}
			pos = append(pos, args[i])
		}
	}
	name := ""
	if len(pos) > 0 {
		name = pos[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	source := rawURL
	if source == "" {
		if name == "" {
			return usageMsg("usage: maio pull <model>")
		}
		m, err := o.marketClient().FindModel(ctx, name)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		name, source, sha = m.Name, m.URL, m.SHA256
	} else if name == "" {
		name = modelNameFromURL(source)
	}
	if name == "" {
		return usageMsg("cannot derive a model name; pass one explicitly")
	}

	var last time.Time
	store := o.store()
	e, err := store.Install(name, source, sha, func(done, total int64) {
		if time.Since(last) < 300*time.Millisecond && (total <= 0 || done < total) {
			return
		}
		last = time.Now()
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\r%s / %s (%.0f%%)", humanSize(done), humanSize(total), 100*float64(done)/float64(total))
		} else {
			fmt.Fprintf(os.Stderr, "\r%s", humanSize(done))
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("pulled %s -> %s (%s)\n", e.Name, e.AbsPath, humanSize(e.Size))
	return 0
}

func modelNameFromURL(source string) string {
	if u, err := url.Parse(source); err == nil && u.Path != "" {
		source = u.Path
	}
	base := strings.TrimSuffix(path.Base(source), ".gguf")
	base = strings.ToLower(strings.ReplaceAll(base, "_", "-"))
	for _, q := range []string{"-q4", "-q5", "-q6", "-q8"} {
		if idx := strings.Index(base, q); idx > 0 {
			base = base[:idx]
		}
	}
	return base
}

func (o *options) cmdPlugin(args []string) int {
	return o.cmdPlugins(args)
}

func (o *options) cmdPlugins(args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		return o.pluginList(args[1:])
	case "remove", "rm", "uninstall":
		return o.pluginRemove(args[1:])
	case "update", "upgrade":
		return o.pluginUpdate(args[1:])
	case "-h", "--help":
		fmt.Println("usage: maio plugins [list|remove|update] [name]")
		return 0
	default:
		return usageMsg("unknown plugins command " + args[0])
	}
}

func (o *options) pluginList(args []string) int {
	list, problems := o.pluginManager().List()
	if o.json {
		return o.printJSON(map[string]any{"plugins": list, "problems": errStrings(problems)})
	}
	if len(list) == 0 {
		fmt.Printf("no plugins in %s\n", o.pluginManager().Dir)
		return 0
	}
	fmt.Printf("%-24s %-10s %-16s %s\n", "NAME", "VERSION", "AUTHOR", "REPO")
	for _, p := range list {
		fmt.Printf("%-24s %-10s %-16s %s\n", truncate(p.Name, 24), truncate(p.Version, 10), truncate(p.Author, 16), p.Repo)
	}
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "warning: %v\n", p)
	}
	return 0
}

func (o *options) pluginRemove(args []string) int {
	if len(args) == 0 {
		return usageMsg("usage: maio plugins remove <name> [--yes]")
	}
	name := args[0]
	yes := false
	for _, a := range args[1:] {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	mgr := o.pluginManager()
	inst, err := mgr.Get(name)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if !yes {
		fmt.Printf("remove plugin %s and %s? [y/N] ", inst.Name, inst.Dir)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("cancelled")
			return 0
		}
	}
	if err := mgr.Remove(name); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("removed plugin %s\n", inst.Name)
	return 0
}

func (o *options) pluginUpdate(args []string) int {
	if len(args) == 0 {
		return usageMsg("usage: maio plugins update <name>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	inst, err := o.pluginManager().Update(ctx, args[0])
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("updated plugin %s to %s\n", inst.Name, inst.Version)
	return 0
}

func (o *options) cmdMarket(args []string) int {
	modelsOnly, pluginsOnly, refresh := false, false, false
	for _, a := range args {
		switch a {
		case "--models", "--model":
			modelsOnly = true
		case "--plugins", "--plugin":
			pluginsOnly = true
		case "--refresh":
			refresh = true
		case "-h", "--help":
			fmt.Println("usage: maio market [--plugins] [--models] [--refresh]")
			return 0
		default:
			return usageMsg("unknown argument " + a)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := o.marketClient()
	if refresh {
		client.Refresh()
	}
	if o.json {
		out := map[string]any{"base_url": client.BaseURL}
		if !modelsOnly {
			p, err := client.Plugins(ctx)
			if err != nil {
				errs.Print(os.Stderr, err)
				return errs.ExitCode(err)
			}
			out["plugins"] = p
		}
		if !pluginsOnly {
			m, err := client.Models(ctx)
			if err != nil {
				errs.Print(os.Stderr, err)
				return errs.ExitCode(err)
			}
			out["models"] = m
		}
		return o.printJSON(out)
	}
	fmt.Printf("market: %s\n", client.BaseURL)
	if !modelsOnly {
		list, err := client.Plugins(ctx)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println("\nPLUGINS")
		if len(list) == 0 {
			fmt.Println("  (empty)")
		}
		for _, p := range list {
			fmt.Printf("  %-24s %-16s %s\n", p.Name, p.Author, p.Description)
		}
	}
	if !pluginsOnly {
		list, err := client.Models(ctx)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println("\nMODELS")
		if len(list) == 0 {
			fmt.Println("  (empty)")
		}
		for _, m := range list {
			fmt.Printf("  %-24s %-10s %s\n", m.Name, m.Quant, m.Description)
		}
	}
	fmt.Println("\ninstall a plugin: maio install <name>")
	fmt.Println("pull a model:     maio pull <name>")
	return 0
}

func (o *options) cmdSearch(args []string) int {
	if len(args) == 0 {
		return usageMsg("usage: maio search <query>")
	}
	q := strings.ToLower(strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := o.marketClient()
	plugins, perr := client.Plugins(ctx)
	models, merr := client.Models(ctx)
	if perr != nil && merr != nil {
		errs.Print(os.Stderr, perr)
		return errs.ExitCode(perr)
	}
	for _, p := range plugins {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Author+" "+p.Description), q) {
			fmt.Printf("plugin  %-24s %-16s %s\n", p.Name, p.Author, p.Description)
		}
	}
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.Name+" "+m.Author+" "+m.Description), q) {
			fmt.Printf("model   %-24s %-10s %s\n", m.Name, m.Quant, m.Description)
		}
	}
	return 0
}

// cmdWeb installs or inspects the optional web console.
func (o *options) cmdWeb(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: maio web install|status|remove [--force]")
		return 0
	}
	dataDir := o.conf.Agent.DataDir
	switch args[0] {
	case "install":
		force := false
		for _, a := range args[1:] {
			switch a {
			case "--force", "-f":
				force = true
			case "-h", "--help":
				fmt.Println("usage: maio web install [--force]")
				return 0
			default:
				return usageMsg("unknown argument " + a)
			}
		}
		dir, err := webui.Install(dataDir, force)
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Printf("web console installed: %s\n", dir)
		fmt.Printf("open: http://127.0.0.1:%d/\n", o.conf.Cluster.Port)
		return 0
	case "remove", "uninstall":
		if err := webui.Remove(dataDir); err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println("web console removed")
		return 0
	case "status":
		dir := webui.Dir(dataDir)
		if webui.Installed(dataDir) {
			fmt.Printf("web console: installed\n  dir: %s\n  url: http://127.0.0.1:%d/\n", dir, o.conf.Cluster.Port)
		} else {
			fmt.Printf("web console: not installed\n  dir: %s\n  install: maio web install\n", dir)
		}
		return 0
	default:
		return usageMsg("unknown web command " + args[0])
	}
}
