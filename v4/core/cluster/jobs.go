package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

// JobRequest is the API body for POST /v1/jobs.
type JobRequest struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	Mode      string   `json:"mode,omitempty"` // auto, shard, route, local
	MaxTokens int      `json:"max_tokens,omitempty"`
	Context   int      `json:"ctx,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	System    string   `json:"system,omitempty"`
	Owner     string   `json:"owner,omitempty"`
}

func (c *Coordinator) createJob(req JobRequest) (*Job, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, errs.New(errs.Usage, "model is required")
	}
	if req.Prompt == "" {
		return nil, errs.New(errs.Usage, "prompt is required")
	}
	if len(req.Prompt) > 1<<20 {
		return nil, errs.New(errs.Usage, "prompt is too large (max 1 MiB)")
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 256
	}
	if req.MaxTokens < 0 || req.MaxTokens > 32768 {
		return nil, errs.New(errs.Usage, "max_tokens must be 0..32768")
	}
	if req.Mode == "" {
		req.Mode = "auto"
	}
	switch req.Mode {
	case "auto", "shard", "route", "local":
	default:
		return nil, errs.New(errs.Usage, "unknown mode %q (use auto, shard, route or local)", req.Mode)
	}
	c.mu.Lock()
	runner := c.runner
	c.mu.Unlock()
	if runner == nil {
		return nil, errs.New(errs.Unavailable, "no inference runner is configured").
			WithHint("start the coordinator with \"maio cluster up\"")
	}
	job := &Job{
		ID: NewID("job_"), Model: req.Model, Mode: req.Mode, Prompt: req.Prompt, MaxTokens: req.MaxTokens,
		State: JobQueued, CreatedAt: time.Now(), Owner: req.Owner,
	}
	c.mu.Lock()
	c.jobs[job.ID] = job
	c.jobCancel[job.ID] = nil
	c.mu.Unlock()
	c.broadcast(job.ID, JobEvent{Type: "state", State: JobQueued, Job: c.snapshot(job.ID)})

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.jobCancel[job.ID] = cancel
	c.mu.Unlock()
	go c.runJob(ctx, job.ID, runner)
	return c.snapshot(job.ID), nil
}

func (c *Coordinator) runJob(ctx context.Context, id string, runner JobRunner) {
	job := c.snapshot(id)
	if job == nil {
		return
	}
	c.updateJob(id, func(j *Job) {
		j.State = JobRunning
		j.StartedAt = time.Now()
	})
	c.broadcast(id, JobEvent{Type: "state", State: JobRunning, Job: c.snapshot(id)})

	emit := func(ev JobEvent) {
		if ev.Text != "" || ev.Tokens > 0 {
			c.updateJob(id, func(j *Job) {
				j.Output += ev.Text
				j.Tokens = ev.Tokens
				if len(j.Output) > 262144 {
					j.Output = j.Output[:262144]
				}
			})
		}
		if ev.Job == nil {
			ev.Job = c.snapshot(id)
		}
		c.broadcast(id, ev)
	}
	err := runner.Run(ctx, job, emit)
	now := time.Now()
	c.mu.Lock()
	j := c.jobs[id]
	if j != nil {
		j.EndedAt = now
		switch {
		case err == nil:
			j.State = JobDone
		case ctx.Err() != nil:
			j.State = JobCanceled
		default:
			j.State = JobFailed
			j.Error = err.Error()
		}
	}
	doneJob := Job{}
	doneState := JobQueued
	doneErr := ""
	if j != nil {
		doneJob = *j
		doneState = j.State
		doneErr = j.Error
	}
	c.mu.Unlock()
	if j != nil {
		c.broadcast(id, JobEvent{Type: "done", State: doneState, Error: doneErr, Job: c.snapshot(id)})
	}
	if c.OnJobFinished != nil {
		c.OnJobFinished(doneJob)
	}
}

func (c *Coordinator) snapshot(id string) *Job {
	c.mu.RLock()
	defer c.mu.RUnlock()
	j := c.jobs[id]
	if j == nil {
		return nil
	}
	cp := *j
	if j.Plan != nil {
		p := *j.Plan
		p.Placements = append([]Placement(nil), j.Plan.Placements...)
		cp.Plan = &p
	}
	return &cp
}

func (c *Coordinator) updateJob(id string, fn func(*Job)) {
	c.mu.Lock()
	if j := c.jobs[id]; j != nil {
		fn(j)
	}
	c.mu.Unlock()
}

func (c *Coordinator) broadcast(id string, ev JobEvent) {
	c.mu.Lock()
	subs := c.jobSubs[id]
	for ch := range subs {
		select {
		case ch <- ev:
		default:
			// A slow UI must not stall inference.
		}
	}
	c.mu.Unlock()
}

func (c *Coordinator) subscribe(id string) (<-chan JobEvent, func(), bool) {
	ch := make(chan JobEvent, 128)
	c.mu.Lock()
	if _, ok := c.jobs[id]; !ok {
		c.mu.Unlock()
		return nil, nil, false
	}
	if c.jobSubs[id] == nil {
		c.jobSubs[id] = map[chan JobEvent]struct{}{}
	}
	c.jobSubs[id][ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if subs := c.jobSubs[id]; subs != nil {
			delete(subs, ch)
		}
		c.mu.Unlock()
	}, true
}

func (c *Coordinator) cancelJob(id string) error {
	c.mu.Lock()
	_, ok := c.jobs[id]
	cancel := c.jobCancel[id]
	runner := c.runner
	c.mu.Unlock()
	if !ok {
		return errs.New(errs.NotFound, "job %s not found", id)
	}
	if cancel != nil {
		cancel()
	}
	if runner != nil {
		runner.Cancel(id)
	}
	return nil
}

func (c *Coordinator) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req JobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
		return
	}
	req.Owner = clientIP(r)
	job, err := c.createJob(req)
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (c *Coordinator) handleListJobs(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	out := make([]Job, 0, len(c.jobs))
	for _, j := range c.jobs {
		out = append(out, *j)
	}
	c.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (c *Coordinator) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j := c.snapshot(r.PathValue("id"))
	if j == nil {
		writeErr(w, http.StatusNotFound, "not_found", "job not found", "")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (c *Coordinator) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if err := c.cancelJob(r.PathValue("id")); err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Coordinator) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job := c.snapshot(id)
	if job == nil {
		writeErr(w, http.StatusNotFound, "not_found", "job not found", "")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(ev JobEvent) error {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	_ = send(JobEvent{Type: "state", State: job.State, Job: job})
	if job.State == JobDone || job.State == JobFailed || job.State == JobCanceled {
		return
	}
	ch, unsubscribe, ok := c.subscribe(id)
	if !ok {
		return
	}
	defer unsubscribe()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if err := send(ev); err != nil {
				return
			}
			if ev.Type == "done" {
				return
			}
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// AttachPlan records the placement on a running job.
func (c *Coordinator) AttachPlan(id string, plan *Plan) {
	c.mu.Lock()
	if j := c.jobs[id]; j != nil {
		j.Plan = plan
	}
	c.mu.Unlock()
	if plan != nil {
		c.broadcast(id, JobEvent{Type: "plan", Job: c.snapshot(id)})
	}
}
