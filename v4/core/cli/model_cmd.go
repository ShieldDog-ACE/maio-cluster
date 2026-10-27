package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/model"
)

func (o *options) cmdModel(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: maio model list|install|show|rm|path")
		return 0
	}
	switch args[0] {
	case "list", "ls":
		return o.modelList(args[1:])
	case "install", "pull", "add":
		return o.modelInstall(args[1:])
	case "show", "info":
		return o.modelShow(args[1:])
	case "rm", "remove", "uninstall":
		return o.modelRemove(args[1:])
	case "path":
		if len(args) < 2 {
			errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio model path <name>"))
			return 2
		}
		e, err := o.store().Get(args[1])
		if err != nil {
			errs.Print(os.Stderr, err)
			return errs.ExitCode(err)
		}
		fmt.Println(e.AbsPath)
		return 0
	case "-h", "--help":
		fmt.Println("usage: maio model <command>")
		fmt.Println("  list                     installed models")
		fmt.Println("  install <name> --url X   register or download a GGUF model")
		fmt.Println("  show <name>              model details")
		fmt.Println("  rm <name>                delete the model directory")
		fmt.Println("  path <name>              print the GGUF path")
		return 0
	default:
		errs.Print(os.Stderr, errs.New(errs.Usage, "unknown model command %q", args[0]))
		return 2
	}
}

func (o *options) modelList(args []string) int {
	entries, problems := o.store().List()
	if o.json {
		return o.printJSON(map[string]any{"models": entries, "problems": errStrings(problems)})
	}
	if len(entries) == 0 {
		fmt.Printf("no models in %s\n", o.conf.Agent.ModelsDir)
		return 0
	}
	fmt.Printf("%-24s %-8s %-10s %10s %s\n", "NAME", "QUANT", "LAYERS", "SIZE", "PATH")
	for _, e := range entries {
		quant, layers := "-", 0
		if e.Meta != nil {
			quant = e.Meta.Quant
			layers = e.Meta.Layers
		}
		fmt.Printf("%-24s %-8s %-10d %10s %s\n", truncate(e.Name, 24), quant, layers, humanSize(e.Size), e.AbsPath)
	}
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "warning: %v\n", p)
	}
	return 0
}

func (o *options) modelInstall(args []string) int {
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
			fmt.Println("usage: maio model install <name> --url <url-or-path> [--sha256 HEX]")
			return 0
		default:
			if strings.HasPrefix(args[i], "--") {
				return usageMsg("unknown argument " + args[i])
			}
			pos = append(pos, args[i])
		}
	}
	if len(pos) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio model install <name> --url <url-or-path>"))
		return 2
	}
	name := pos[0]
	src := rawURL
	if src == "" && len(pos) > 1 {
		src = pos[1]
	}
	if src == "" {
		errs.Print(os.Stderr, errs.New(errs.Usage, "a model source is required").
			WithHint("pass --url https://... or --url /path/to/model.gguf"))
		return 2
	}
	store := o.store()
	var last time.Time
	e, err := store.Install(name, src, sha, func(done, total int64) {
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
	fmt.Printf("installed %s -> %s (%s)\n", e.Name, e.AbsPath, humanSize(e.Size))
	return 0
}

func (o *options) modelShow(args []string) int {
	if len(args) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio model show <name>"))
		return 2
	}
	e, err := o.store().Get(args[0])
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		return o.printJSON(e)
	}
	fmt.Printf("name:    %s\n", e.Name)
	fmt.Printf("format:  %s\n", e.Format)
	fmt.Printf("path:    %s\n", e.AbsPath)
	fmt.Printf("size:    %s\n", humanSize(e.Size))
	if e.Meta != nil {
		m := e.Meta
		fmt.Printf("gguf:    v%d, %s, %d layers, ctx %d, embd %d, quant %s\n",
			m.Version, m.Arch, m.Layers, m.Context, m.Embedding, m.Quant)
	}
	return 0
}

func (o *options) modelRemove(args []string) int {
	if len(args) == 0 {
		errs.Print(os.Stderr, errs.New(errs.Usage, "usage: maio model rm <name> [--yes]"))
		return 2
	}
	name := args[0]
	yes := false
	for _, a := range args[1:] {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	e, err := o.store().Get(name)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if !yes {
		fmt.Printf("remove %s and %s? [y/N] ", e.Name, e.Dir)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("cancelled")
			return 0
		}
	}
	if err := o.store().Remove(name); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("removed %s\n", e.Name)
	return 0
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v/1024)
}

func errStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

var _ = context.Background
var _ = gguf.Magic
var _ = model.Entry{}
