package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

func (o *options) cmdRun(args []string) int {
	mode := "auto"
	maxTokens := 256
	promptParts := []string{}
	var modelName string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mode":
			if i+1 >= len(args) {
				return usageMsg("--mode requires auto, shard, route or local")
			}
			i++
			mode = args[i]
		case "--max-tokens":
			if i+1 >= len(args) {
				return usageMsg("--max-tokens requires a number")
			}
			i++
			maxTokens, _ = strconv.Atoi(args[i])
		case "--nodes":
			if i+1 < len(args) {
				i++
			}
		case "--help", "-h":
			fmt.Println("usage: maio run <model> <prompt> [--mode auto|shard|route|local] [--max-tokens N]")
			return 0
		default:
			if modelName == "" {
				modelName = args[i]
			} else {
				promptParts = append(promptParts, args[i])
			}
		}
	}
	if modelName == "" || len(promptParts) == 0 {
		return usageMsg("usage: maio run <model> <prompt>")
	}
	prompt := strings.Join(promptParts, " ")
	ctx, cancel := signalContext()
	defer cancel()
	client := o.client()
	job, err := client.CreateJob(ctx, cluster.JobRequest{
		Model: modelName, Prompt: prompt, Mode: mode, MaxTokens: maxTokens,
	})
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		if !streamJSON(ctx, client, job) {
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "job %s (%s)\n", job.ID, job.Mode)
	if !streamText(ctx, client, job) {
		return 1
	}
	return 0
}

func streamText(ctx context.Context, client *cluster.Client, job *cluster.Job) bool {
	ok := true
	printedPlan := false
	var wrote bool
	err := client.StreamJob(ctx, job.ID, func(ev cluster.JobEvent) error {
		switch ev.Type {
		case "token":
			fmt.Print(ev.Text)
			wrote = true
		case "plan":
			if !printedPlan && ev.Job != nil && ev.Job.Plan != nil {
				printedPlan = true
				fmt.Fprintf(os.Stderr, "\nplacement: ")
				for i, p := range ev.Job.Plan.Placements {
					if i > 0 {
						fmt.Fprint(os.Stderr, ", ")
					}
					fmt.Fprintf(os.Stderr, "%s(%d)", p.NodeName, p.Layers)
				}
				fmt.Fprintln(os.Stderr)
			}
		case "done":
			if wrote {
				fmt.Println()
			}
			if ev.State == cluster.JobFailed {
				fmt.Fprintf(os.Stderr, "job failed: %s\n", ev.Error)
				ok = false
			} else if ev.State == cluster.JobCanceled {
				fmt.Fprintln(os.Stderr, "job cancelled")
				ok = false
			} else if ev.Job != nil {
				fmt.Fprintf(os.Stderr, "done: %d chunks\n", ev.Job.Tokens)
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "stream error: %v\n", err)
		ok = false
	}
	return ok
}

func streamJSON(ctx context.Context, client *cluster.Client, job *cluster.Job) bool {
	ok := true
	err := client.StreamJob(ctx, job.ID, func(ev cluster.JobEvent) error {
		switch ev.Type {
		case "token":
			clusterJobWriter{}.write(ev)
		case "done":
			if ev.State == cluster.JobFailed || ev.State == cluster.JobCanceled {
				ok = false
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		ok = false
	}
	return ok
}

type clusterJobWriter struct{}

func (clusterJobWriter) write(ev cluster.JobEvent) {
	b, _ := jsonMarshal(map[string]any{"type": ev.Type, "text": ev.Text, "tokens": ev.Tokens})
	fmt.Println(string(b))
}

func (o *options) cmdJobs(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	jobs, err := o.client().ListJobs(ctx)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		return o.printJSON(map[string]any{"jobs": jobs})
	}
	fmt.Printf("%-22s %-18s %-8s %-8s %s\n", "JOB", "MODEL", "MODE", "STATE", "OUTPUT")
	for _, j := range jobs {
		fmt.Printf("%-22s %-18s %-8s %-8s %s\n", shortID(j.ID), truncate(j.Model, 18), j.Mode, j.State, truncate(strings.ReplaceAll(j.Output, "\n", " "), 40))
	}
	return 0
}

func (o *options) cmdJob(args []string) int {
	if len(args) == 0 {
		return o.cmdJobs(nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := o.client().GetJob(ctx, args[0])
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	if o.json {
		return o.printJSON(job)
	}
	fmt.Printf("id:      %s\nmodel:   %s\nmode:    %s\nstate:   %s\ncreated: %s\n", job.ID, job.Model, job.Mode, job.State, job.CreatedAt.Format(time.RFC3339))
	if job.Plan != nil {
		fmt.Printf("plan:    %d layers across %d nodes\n", job.Plan.Layers, len(job.Plan.Placements))
	}
	if job.Error != "" {
		fmt.Printf("error:   %s\n", job.Error)
	}
	if job.Output != "" {
		fmt.Println("output:")
		fmt.Println(job.Output)
	}
	return 0
}

func (o *options) cmdCancel(args []string) int {
	if len(args) == 0 {
		return usageMsg("usage: maio cancel <job-id>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := o.client().CancelJob(ctx, args[0]); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Println("cancelled")
	return 0
}

func usageMsg(msg string) int {
	e := errs.New(errs.Usage, "%s", msg)
	errs.Print(os.Stderr, e)
	return errs.ExitCode(e)
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
