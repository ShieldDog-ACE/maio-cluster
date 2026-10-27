package cluster

import (
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/hardware"
)

func plannerNode(id string, vram uint64, rpc bool) Node {
	return Node{
		ID: id, Name: id, State: Online, Compute: true,
		Host: "192.168.1.10", Protocol: 1, Role: "worker",
		Hardware: hardware.Info{MemFree: 8 << 30, GPUs: []hardware.GPU{{Name: "gpu", Backend: "cuda", VRAMTotal: vram, VRAMFree: vram}}},
		RPC:      RPCStatus{Available: rpc},
	}
}

func TestBuildPlanSplitsLayers(t *testing.T) {
	nodes := []Node{
		plannerNode("a", 16<<30, true),
		plannerNode("b", 8<<30, true),
	}
	meta := ModelMeta{Name: "m", Layers: 24, Context: 4096, Size: 1 << 30}
	plan, err := BuildPlan(meta, nodes, PlanOptions{RequireRPC: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Placements) != 2 {
		t.Fatalf("want 2 placements, got %d", len(plan.Placements))
	}
	sum := 0
	for _, p := range plan.Placements {
		sum += p.Layers
		if p.Layers < 1 {
			t.Fatalf("node %s got %d layers", p.NodeName, p.Layers)
		}
	}
	if sum != 24 {
		t.Fatalf("layers sum %d, want 24", sum)
	}
	if plan.Placements[0].Layers <= plan.Placements[1].Layers {
		t.Fatalf("largest node should get more layers: %+v", plan.Placements)
	}
}

func TestBuildPlanOneNodeWarns(t *testing.T) {
	plan, err := BuildPlan(ModelMeta{Name: "m", Layers: 8, Size: 100 << 20},
		[]Node{plannerNode("a", 16<<30, true)}, PlanOptions{RequireRPC: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected a single-node warning")
	}
}

func TestBuildPlanRejectsNoRPC(t *testing.T) {
	_, err := BuildPlan(ModelMeta{Name: "m", Layers: 8, Size: 100 << 20},
		[]Node{plannerNode("a", 16<<30, false)}, PlanOptions{RequireRPC: true})
	if err == nil {
		t.Fatal("expected no-RPC failure")
	}
}

func TestBuildPlanRejectsTooSmall(t *testing.T) {
	_, err := BuildPlan(ModelMeta{Name: "m", Layers: 8, Size: 100 << 30},
		[]Node{plannerNode("a", 8<<30, true)}, PlanOptions{RequireRPC: true})
	if err == nil {
		t.Fatal("expected capacity failure")
	}
}
