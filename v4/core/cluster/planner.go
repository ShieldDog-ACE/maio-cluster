package cluster

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

// ModelMeta is what the planner needs to know about a model.
type ModelMeta struct {
	Name    string `json:"name"`
	Layers  int    `json:"layers"`
	Context int    `json:"context"`
	Size    int64  `json:"size_bytes"`
	Quant   string `json:"quant,omitempty"`
}

// PlanOptions constrains placement.
type PlanOptions struct {
	// Exclude node IDs.
	Exclude []string
	// MaxNodes caps the number of shards (0 = 8).
	MaxNodes int
	// MinFreeBytes is the minimum usable capacity per node.
	MinFreeBytes uint64
	// Frontend is the node that runs llama-server and holds the model file.
	Frontend string
	// RequireRPC rejects nodes without a working ggml-rpc-server.
	RequireRPC bool
}

// DefaultModelMeta returns conservative defaults when the GGUF header is not
// available (for example when planning from a remote node's advertisement).
func DefaultModelMeta(name string, layers int, size int64) ModelMeta {
	if layers <= 0 {
		layers = 32
	}
	return ModelMeta{Name: name, Layers: layers, Size: size, Context: 4096}
}

// BuildPlan distributes model layers across nodes in proportion to usable
// memory. llama.cpp's RPC backend treats each ggml-rpc-server as a device, so
// one placement is one device.
func BuildPlan(meta ModelMeta, nodes []Node, opts PlanOptions) (*Plan, error) {
	if meta.Layers <= 0 {
		return nil, errs.New(errs.Hardware, "model %s does not report a layer count", meta.Name).
			WithHint("read the GGUF header with \"maio model show %s\", or pass --layers", meta.Name)
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 8
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = 512 << 20
	}
	excluded := map[string]bool{}
	for _, id := range opts.Exclude {
		excluded[id] = true
	}
	var candidates []Node
	reasons := []string{}
	for _, n := range nodes {
		if excluded[n.ID] {
			continue
		}
		if n.State != Online {
			reasons = append(reasons, fmt.Sprintf("%s is %s", n.Name, n.State))
			continue
		}
		if !n.Compute {
			reasons = append(reasons, fmt.Sprintf("%s does not accept compute", n.Name))
			continue
		}
		if opts.RequireRPC && !n.RPC.Available {
			reasons = append(reasons, fmt.Sprintf("%s has no RPC backend", n.Name))
			continue
		}
		if n.Capacity() < opts.MinFreeBytes {
			reasons = append(reasons, fmt.Sprintf("%s has only %s usable memory", n.Name, humanBytes(n.Capacity())))
			continue
		}
		candidates = append(candidates, n)
	}
	if len(candidates) == 0 {
		return nil, errs.New(errs.Unavailable, "no node can host model %s", meta.Name).
			WithHint("start an agent with a working ggml-rpc-server and enough free memory").
			WithHint("details: %s", strings.Join(reasons, "; "))
	}
	sort.Slice(candidates, func(i, j int) bool {
		ci, cj := candidates[i].Capacity(), candidates[j].Capacity()
		if ci != cj {
			return ci > cj
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(candidates) > opts.MaxNodes {
		candidates = candidates[:opts.MaxNodes]
	}
	if len(candidates) > meta.Layers {
		candidates = candidates[:meta.Layers]
	}
	if len(candidates) == 0 {
		return nil, errs.New(errs.Unavailable, "not enough nodes to split %d layers", meta.Layers)
	}

	// If a frontend was requested it must participate; otherwise prefer the
	// coordinator so the model file it already holds can be used.
	if opts.Frontend != "" {
		found := false
		for _, n := range candidates {
			if n.ID == opts.Frontend {
				found = true
			}
		}
		if !found {
			if n, ok := findNode(nodes, opts.Frontend); ok && n.State == Online && n.Compute {
				candidates = append([]Node{n}, candidates...)
				if len(candidates) > opts.MaxNodes {
					candidates = candidates[:opts.MaxNodes]
				}
			} else {
				return nil, errs.New(errs.Unavailable, "frontend node %s cannot participate", opts.Frontend)
			}
		}
	}

	total := uint64(0)
	for _, n := range candidates {
		total += n.Capacity()
	}
	if total == 0 {
		return nil, errs.New(errs.Hardware, "total usable memory is zero")
	}
	// Reserve 20% headroom for KV cache and graph buffers. The model must fit.
	need := uint64(meta.Size)
	if need > 0 && total*8/10 < need {
		return nil, errs.New(errs.Hardware, "not enough aggregate memory for %s", meta.Name).
			WithHint("model is %s; nodes offer %s usable", humanBytes(need), humanBytes(total)).
			WithHint("add a node or use a smaller quantization")
	}

	layers := meta.Layers
	shares := make([]int, len(candidates))
	assigned := 0
	for i, n := range candidates {
		sh := int(float64(layers) * float64(n.Capacity()) / float64(total))
		if sh < 1 {
			sh = 1
		}
		shares[i] = sh
		assigned += sh
	}
	// Fix rounding drift on the largest node.
	drift := layers - assigned
	if drift != 0 {
		target := 0
		for i := range candidates {
			if candidates[i].Capacity() > candidates[target].Capacity() {
				target = i
			}
		}
		shares[target] += drift
		if shares[target] < 1 {
			shares[target] = 1
		}
	}

	plan := &Plan{
		Model: meta.Name, Layers: layers, Context: meta.Context,
		CreatedAt: time.Now(), Frontend: opts.Frontend,
	}
	var weightSum float64
	for i, n := range candidates {
		w := float64(shares[i])
		weightSum += w
		p := Placement{
			NodeID: n.ID, NodeName: n.Name, Host: n.Host,
			Weight: w, Layers: shares[i], Capacity: n.Capacity(),
			RPCPort: 0,
		}
		plan.Placements = append(plan.Placements, p)
	}
	plan.TotalWeight = weightSum
	if len(plan.Placements) == 1 {
		plan.Warnings = append(plan.Warnings, "only one node is available; layers are not split across machines")
	}
	if len(reasons) > 0 {
		plan.Warnings = append(plan.Warnings, "excluded: "+strings.Join(reasons, "; "))
	}
	return plan, nil
}

func findNode(nodes []Node, id string) (Node, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (c *Coordinator) handlePlan(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("model")
	var req struct {
		Model    string `json:"model"`
		MaxNodes int    `json:"max_nodes"`
		Frontend string `json:"frontend"`
	}
	if r.Method == http.MethodPost {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error(), "")
			return
		}
		name = req.Model
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "model is required", "")
		return
	}
	meta := ModelMeta{Name: name}
	if c.Models != nil {
		if e, err := c.Models.Get(name); err == nil {
			meta.Size = e.Size
			if e.Meta != nil {
				meta.Layers = e.Meta.Layers
				meta.Context = e.Meta.Context
				meta.Quant = e.Meta.Quant
			}
		}
	}
	if meta.Layers == 0 {
		for _, n := range c.Nodes() {
			for _, m := range n.Models {
				if m.Name == name {
					meta.Layers = m.Layers
					meta.Size = m.Size
					meta.Quant = m.Quant
				}
			}
		}
	}
	if meta.Layers == 0 {
		meta = DefaultModelMeta(name, 0, meta.Size)
	}
	plan, err := BuildPlan(meta, c.Nodes(), PlanOptions{
		MaxNodes: req.MaxNodes, Frontend: req.Frontend, RequireRPC: true,
	})
	if err != nil {
		writeErrCode(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
