package cluster

import (
	"net"
	"strconv"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/netinfo"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// NodeState is the liveness of a member.
type NodeState string

const (
	Online   NodeState = "online"
	Stale    NodeState = "stale"
	Offline  NodeState = "offline"
	Draining NodeState = "draining"
)

// ModelRef is a model advertised by a node.
type ModelRef struct {
	Name   string `json:"name"`
	Size   int64  `json:"size_bytes"`
	Layers int    `json:"layers"`
	Quant  string `json:"quant,omitempty"`
}

// RPCStatus reports whether a node can accept llama.cpp RPC layers.
type RPCStatus struct {
	Available bool   `json:"available"`
	Binary    string `json:"binary,omitempty"`
	Devices   int    `json:"devices"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Node is one cluster member.
type Node struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Role      string            `json:"role"` // coordinator or worker
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Version   string            `json:"version"`
	Protocol  int               `json:"protocol"`
	Host      string            `json:"host"`
	APIPort   int               `json:"api_port"`
	Hardware  hardware.Info     `json:"hardware"`
	Metrics   hardware.Metrics  `json:"metrics"`
	Links     []netinfo.Address `json:"links,omitempty"`
	Models    []ModelRef        `json:"models"`
	RPC       RPCStatus         `json:"rpc"`
	State     NodeState         `json:"state"`
	JoinedAt  time.Time         `json:"joined_at"`
	LastSeen  time.Time         `json:"last_seen"`
	ClusterID string            `json:"cluster_id"`
	Compute   bool              `json:"compute"`
}

// Capacity estimates usable shard memory.
func (n *Node) Capacity() uint64 { return n.Hardware.CapacityBytes() }

// Addr returns host:port.
func (n *Node) Addr() string { return net.JoinHostPort(n.Host, strconv.Itoa(n.APIPort)) }

// HasModel reports whether the node advertises name.
func (n *Node) HasModel(name string) bool {
	for _, m := range n.Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// PublicNode strips coordinator-only fields before sending.
func (n *Node) Clone() Node {
	c := *n
	c.Links = append([]netinfo.Address(nil), n.Links...)
	c.Models = append([]ModelRef(nil), n.Models...)
	c.Hardware.GPUs = append([]hardware.GPU(nil), n.Hardware.GPUs...)
	c.Hardware.Warnings = append([]string(nil), n.Hardware.Warnings...)
	c.Metrics.GPUs = append([]hardware.GPUMetrics(nil), n.Metrics.GPUs...)
	return c
}

// ClusterInfo is the public description of a cluster.
type ClusterInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Protocol    int       `json:"protocol"`
	Coordinator string    `json:"coordinator"`
	Port        int       `json:"port"`
	Host        string    `json:"host,omitempty"`
	Nodes       int       `json:"nodes"`
	StartedAt   time.Time `json:"started_at"`
	TLS         bool      `json:"tls"`
}

// DefaultNode fills version and protocol fields.
func DefaultNode() Node {
	return Node{Version: version.Version, Protocol: version.Protocol, State: Online}
}

// JoinRequest is sent by a worker.
type JoinRequest struct {
	Node   Node   `json:"node"`
	Secret string `json:"secret,omitempty"`
}

// JoinResponse is returned by the coordinator.
type JoinResponse struct {
	OK          bool        `json:"ok"`
	NodeID      string      `json:"node_id"`
	Cluster     ClusterInfo `json:"cluster"`
	HeartbeatMS int         `json:"heartbeat_ms"`
	Token       string      `json:"token,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// Heartbeat updates liveness and mutable fields.
type Heartbeat struct {
	NodeID  string            `json:"node_id"`
	Load    float64           `json:"load"`
	State   NodeState         `json:"state"`
	Models  []ModelRef        `json:"models"`
	RPC     RPCStatus         `json:"rpc"`
	Metrics hardware.Metrics  `json:"metrics"`
	Links   []netinfo.Address `json:"links,omitempty"`
	Host    string            `json:"host,omitempty"`
	APIPort int               `json:"api_port,omitempty"`
}

// Command is one unit of work pushed to an agent.
type Command struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Args    map[string]string `json:"args,omitempty"`
	Created time.Time         `json:"created"`
	Timeout int               `json:"timeout_seconds"`
}

// CommandResult is returned by an agent.
type CommandResult struct {
	ID    string            `json:"id"`
	OK    bool              `json:"ok"`
	Error string            `json:"error,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

// APIError is the stable error envelope.
type APIError struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}

// Placement is one node's share of a sharded model.
type Placement struct {
	NodeID   string  `json:"node_id"`
	NodeName string  `json:"node_name"`
	Host     string  `json:"host"`
	Weight   float64 `json:"weight"`
	Layers   int     `json:"layers"`
	Capacity uint64  `json:"capacity_bytes"`
	RPCPort  int     `json:"rpc_port"`
	Endpoint string  `json:"endpoint,omitempty"`
	Tunneled bool    `json:"tunneled"`
}

// Plan is a distributed inference placement.
type Plan struct {
	Model       string      `json:"model"`
	Layers      int         `json:"layers"`
	Context     int         `json:"context"`
	Frontend    string      `json:"frontend"`
	Placements  []Placement `json:"placements"`
	TotalWeight float64     `json:"total_weight"`
	Warnings    []string    `json:"warnings,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
}

// JobState is the lifecycle of an inference job.
type JobState string

const (
	JobQueued   JobState = "queued"
	JobRunning  JobState = "running"
	JobDone     JobState = "done"
	JobFailed   JobState = "failed"
	JobCanceled JobState = "canceled"
)

// Job is one distributed inference request.
type Job struct {
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Mode      string    `json:"mode"`
	Prompt    string    `json:"prompt,omitempty"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	State     JobState  `json:"state"`
	Error     string    `json:"error,omitempty"`
	Plan      *Plan     `json:"plan,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Tokens    int       `json:"tokens"`
	Output    string    `json:"output,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	Owner     string    `json:"owner,omitempty"`
}
