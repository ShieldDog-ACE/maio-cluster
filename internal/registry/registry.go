// Package registry scans the model root and lists installed models.
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ShieldDog-ACE/maio--cluster/internal/config"
	"github.com/ShieldDog-ACE/maio--cluster/internal/naming"
)

// Entry is one item of the listing.
type Entry struct {
	Model *config.Model
	Size  int64
}

// Scan walks model directories under root (one maio.toml per directory).
// It returns the listing plus warnings; a broken config does not block other models.
func Scan(root string) ([]Entry, []error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []Entry
	var warns []error
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		m, err := config.LoadModel(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				warns = append(warns, err)
			}
			continue
		}
		var size int64
		if st, err := os.Stat(m.FilePath()); err == nil {
			size = st.Size()
		}
		out = append(out, Entry{Model: m, Size: size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model.Name < out[j].Model.Name })
	return out, warns
}

// Find looks up a model by name (normalized automatically).
func Find(root, rawName string) (*config.Model, error) {
	name, err := naming.Normalize(rawName)
	if err != nil {
		return nil, err
	}
	dir := config.ModelDir(root, name)
	m, err := config.LoadModel(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("model %s is not installed (missing %s)", name, config.ModelConfigPath(dir))
		}
		return nil, err
	}
	return m, nil
}
