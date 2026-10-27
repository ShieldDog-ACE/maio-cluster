package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/cluster"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/discovery"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

type nodeState struct {
	ID          string `json:"id"`
	Coordinator string `json:"coordinator"`
}

type persistedLink struct {
	BaseURL string           `json:"base_url"`
	Code    cluster.JoinCode `json:"code"`
}

func statePath(dir, name string) string { return filepath.Join(dir, name) }

func loadNodeState(dir string) (nodeState, bool) {
	var st nodeState
	raw, err := os.ReadFile(statePath(dir, "node.json"))
	if err != nil || json.Unmarshal(raw, &st) != nil || st.ID == "" {
		return nodeState{}, false
	}
	return st, true
}

func saveNodeState(dir string, st nodeState) {
	_ = os.MkdirAll(dir, 0o700)
	raw, _ := json.MarshalIndent(st, "", "  ")
	_ = writeAtomic(statePath(dir, "node.json"), raw, 0o600)
}

func loadLink(dir string) (link, bool) {
	var pl persistedLink
	raw, err := os.ReadFile(statePath(dir, "link.json"))
	if err != nil || json.Unmarshal(raw, &pl) != nil || pl.BaseURL == "" {
		return link{}, false
	}
	return link{BaseURL: pl.BaseURL, Code: &pl.Code}, true
}

func saveLink(dir string, l link) {
	if l.Code == nil {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	raw, _ := json.MarshalIndent(persistedLink{BaseURL: l.BaseURL, Code: *l.Code}, "", "  ")
	_ = writeAtomic(statePath(dir, "link.json"), raw, 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write %s", path)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return errs.Wrap(errs.Internal, err, "cannot replace %s", path)
	}
	return nil
}

func discoveryFind(ctx context.Context, token string, port int) (discovery.Beacon, error) {
	return discovery.Find(ctx, token, port, 60e9)
}

func discoveryPort(apiPort int) int { return discovery.Port(apiPort) }
