package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/config"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
)

func testCoordinator(t *testing.T) (*Coordinator, context.CancelFunc) {
	t.Helper()
	cfg := &config.Config{
		Cluster: config.Cluster{
			Name: "test", ID: "mc_test", Token: "0123456789abcdef0123456789abcdef",
			Bind: "127.0.0.1", Port: 0, MaxNodes: 8, JoinTimeout: 5, Compute: true,
		},
		Agent: config.Agent{
			Name: "n", DataDir: t.TempDir(), ModelsDir: t.TempDir(),
			RPCPortBase: 50100, MaxJobs: 2, HeartbeatSec: 3, OfflineSec: 15,
		},
	}
	c, err := NewCoordinator(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Start(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		addr := c.Addr()
		if addr == "" {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		client := NewClient(addr, cfg.Cluster.Token)
		ctx, done := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := client.Health(ctx)
		done()
		if err == nil {
			return c, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("coordinator did not start")
	return nil, cancel
}

func TestCoordinatorJoinCommandFlow(t *testing.T) {
	c, cancel := testCoordinator(t)
	defer cancel()
	defer c.Close()
	client := NewClient(c.Addr(), c.cfg.Cluster.Token)

	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	join, err := client.Join(ctx, JoinRequest{Node: Node{
		Name: "worker", Protocol: 1, Host: "192.168.1.9",
		Hardware: hardware.Info{MemFree: 4 << 30, GPUs: []hardware.GPU{{VRAMTotal: 4 << 30, VRAMFree: 4 << 30}}},
		RPC:      RPCStatus{Available: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if join.NodeID == "" {
		t.Fatal("no node id")
	}
	nodes, err := client.Nodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes=%v err=%v", nodes, err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := c.Call(ctx, join.NodeID, "ping", nil, 3*time.Second)
		result <- err
	}()
	cmd, err := c.nextCommand(ctx, join.NodeID)
	if err != nil || cmd == nil {
		t.Fatalf("next command: %v %v", cmd, err)
	}
	if cmd.Kind != "ping" {
		t.Fatalf("kind=%s", cmd.Kind)
	}
	if !c.deliverResult(join.NodeID, CommandResult{ID: cmd.ID, OK: true, Data: map[string]string{"pong": "1"}}) {
		t.Fatal("result not delivered")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not finish")
	}
}

func TestCoordinatorRejectsBadToken(t *testing.T) {
	c, cancel := testCoordinator(t)
	defer cancel()
	defer c.Close()
	client := NewClient(c.Addr(), "wrong")
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if _, err := client.Nodes(ctx); err == nil {
		t.Fatal("bad token accepted")
	}
}
