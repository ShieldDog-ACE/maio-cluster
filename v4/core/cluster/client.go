package cluster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// Client talks to a coordinator API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient builds a client from host:port or an http(s) URL.
func NewClient(base, token string) *Client {
	base = strings.TrimSpace(base)
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")
	return &Client{
		BaseURL: base,
		Token:   token,
		HTTP: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func (c *Client) endpoint(path string) string { return c.BaseURL + path }

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), rdr)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "maio/"+version.Version)
	return req, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return errs.Wrap(errs.Internal, err, "cannot build request")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot reach coordinator at %s", c.BaseURL).
			WithHint("check the LAN/USB/hotspot link and that the coordinator is running")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return errs.Wrap(errs.Network, err, "invalid coordinator response")
	}
	return nil
}

func apiError(resp *http.Response) error {
	var ae APIError
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(raw, &ae)
	if ae.Error == "" {
		ae.Error = strings.TrimSpace(string(raw))
	}
	if ae.Error == "" {
		ae.Error = resp.Status
	}
	code := errs.Internal
	switch resp.StatusCode {
	case http.StatusBadRequest:
		code = errs.Usage
	case http.StatusUnauthorized, http.StatusForbidden:
		code = errs.Auth
	case http.StatusNotFound:
		code = errs.NotFound
	case http.StatusConflict:
		code = errs.Conflict
	case http.StatusServiceUnavailable:
		code = errs.Unavailable
	case http.StatusGatewayTimeout:
		code = errs.Timeout
	}
	e := errs.New(code, "%s", ae.Error)
	if ae.Hint != "" {
		e.WithHint("%s", ae.Hint)
	}
	return e
}

// Health checks liveness.
func (c *Client) Health(ctx context.Context) error {
	var out map[string]any
	return c.do(ctx, http.MethodGet, "/v1/health", nil, &out)
}

// Info returns the public cluster description.
func (c *Client) Info(ctx context.Context) (ClusterInfo, error) {
	var out ClusterInfo
	err := c.do(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, err
}

// Join registers a node.
func (c *Client) Join(ctx context.Context, req JoinRequest) (JoinResponse, error) {
	var out JoinResponse
	err := c.do(ctx, http.MethodPost, "/v1/join", req, &out)
	return out, err
}

// Heartbeat updates membership.
func (c *Client) Heartbeat(ctx context.Context, hb Heartbeat) error {
	return c.do(ctx, http.MethodPost, "/v1/heartbeat", hb, nil)
}

// Nodes lists members.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var out struct {
		Nodes []Node `json:"nodes"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/nodes", nil, &out)
	return out.Nodes, err
}

// Drain marks a node as not accepting new work.
func (c *Client) Drain(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(id)+"/drain", map[string]any{}, nil)
}

// NextCommand long-polls for one command. It returns nil on 204.
func (c *Client) NextCommand(ctx context.Context, nodeID string) (*Command, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/agent/commands/next", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Maio-Node", nodeID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.Network, err, "command poll failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(resp)
	}
	var cmd Command
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cmd); err != nil {
		return nil, errs.Wrap(errs.Network, err, "invalid command response")
	}
	return &cmd, nil
}

// CommandResult reports completion.
func (c *Client) CommandResult(ctx context.Context, nodeID string, res CommandResult) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/agent/commands/result", res)
	if err != nil {
		return err
	}
	req.Header.Set("X-Maio-Node", nodeID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot report command result")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	return nil
}

// CreateJob submits inference.
func (c *Client) CreateJob(ctx context.Context, req JobRequest) (*Job, error) {
	var out Job
	if err := c.do(ctx, http.MethodPost, "/v1/jobs", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetJob fetches one job.
func (c *Client) GetJob(ctx context.Context, id string) (*Job, error) {
	var out Job
	if err := c.do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListJobs lists active and recent jobs.
func (c *Client) ListJobs(ctx context.Context) ([]Job, error) {
	var out struct {
		Jobs []Job `json:"jobs"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/jobs", nil, &out)
	return out.Jobs, err
}

// CancelJob cancels one job.
func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/cancel", map[string]any{}, nil)
}

// StreamJob follows SSE events until a terminal event or ctx ends.
func (c *Client) StreamJob(ctx context.Context, id string, fn func(JobEvent) error) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id)+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errs.Wrap(errs.Network, err, "cannot follow job events")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev JobEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if err := fn(ev); err != nil {
			return err
		}
		if ev.Type == "done" {
			return nil
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return errs.Wrap(errs.Network, err, "job event stream ended")
	}
	return ctx.Err()
}

// Plan previews placement.
func (c *Client) Plan(ctx context.Context, model string, maxNodes int, frontend string) (*Plan, error) {
	var out Plan
	body := map[string]any{"model": model, "max_nodes": maxNodes, "frontend": frontend}
	if err := c.do(ctx, http.MethodPost, "/v1/plan", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Distribute copies one coordinator-local model to workers.
func (c *Client) Distribute(ctx context.Context, model string, nodes []string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodPost, "/v1/models/distribute", map[string]any{"name": model, "nodes": nodes}, &out)
	return out, err
}

// Models lists models advertised by members.
func (c *Client) Models(ctx context.Context) ([]map[string]any, error) {
	var out struct {
		Models []map[string]any `json:"models"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/models", nil, &out)
	return out.Models, err
}

// DownloadFile fetches a coordinator-served model file into w.
func (c *Client) DownloadFile(ctx context.Context, name string, w io.Writer) (int64, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/models/"+url.PathEscape(name)+"/file", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, errs.Wrap(errs.Network, err, "cannot download model")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, apiError(resp)
	}
	return io.Copy(w, resp.Body)
}

var _ = fmt.Sprintf
