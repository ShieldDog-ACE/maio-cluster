// Package resident persists the set of models pinned by "maio load". The
// service reads this file on startup and reloads those models, so a reboot or
// crash does not silently drop a resident inference set.
package resident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
)

// FileName is the resident state file inside the run directory.
const FileName = "resident.json"

type state struct {
	Models []string `json:"models"`
}

// Path returns the resident file path for a run directory.
func Path(runDir string) string { return filepath.Join(runDir, FileName) }

// Load reads and validates the resident list. A missing file means no resident
// models. Invalid names are ignored so a hand-edited file cannot break boot.
func Load(runDir string) ([]string, error) {
	data, err := os.ReadFile(Path(runDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.Wrap(errs.IO, err, "cannot read the resident model state %s", Path(runDir))
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, errs.Wrap(errs.IO, err, "invalid resident model state %s", Path(runDir)).
			WithHint("delete %s and restart the service", Path(runDir))
	}
	seen := make(map[string]bool, len(st.Models))
	out := make([]string, 0, len(st.Models))
	for _, name := range st.Models {
		name = naming.NormalizeLoose(name)
		if name == "" || seen[name] || naming.Validate(name) != nil {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Save atomically writes the resident list.
func Save(runDir string, names []string) error {
	clean := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = naming.NormalizeLoose(name)
		if name == "" || seen[name] || naming.Validate(name) != nil {
			continue
		}
		seen[name] = true
		clean = append(clean, name)
	}
	sort.Strings(clean)
	data, err := json.MarshalIndent(state{Models: clean}, "", "  ")
	if err != nil {
		return errs.Wrap(errs.IO, err, "cannot serialize the resident model state")
	}
	data = append(data, '\n')
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create the run directory %s", runDir)
	}
	tmp := Path(runDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write the resident model state %s", tmp)
	}
	if err := os.Rename(tmp, Path(runDir)); err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(errs.IO, err, "cannot replace the resident model state %s", Path(runDir))
	}
	return nil
}

// Add pins a model and returns the new list.
func Add(runDir, name string) ([]string, error) {
	names, err := Load(runDir)
	if err != nil {
		return nil, err
	}
	normalized := naming.NormalizeLoose(name)
	for _, n := range names {
		if n == normalized {
			return names, nil
		}
	}
	names = append(names, normalized)
	return names, Save(runDir, names)
}

// Remove unpins a model and returns the new list.
func Remove(runDir, name string) ([]string, error) {
	names, err := Load(runDir)
	if err != nil {
		return nil, err
	}
	normalized := naming.NormalizeLoose(name)
	out := names[:0]
	for _, n := range names {
		if n != normalized {
			out = append(out, n)
		}
	}
	return out, Save(runDir, out)
}
