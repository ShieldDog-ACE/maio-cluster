// Package registry scans the model root and lists installed models.
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
)

// Entry is one item of the listing.
type Entry struct {
	Model   *config.Model
	Size    int64
	Missing bool
	Err     error
}

// Scan walks model directories under root (one maio.toml per directory).
// It returns the listing plus warnings; a broken config does not block other
// models. A missing root is treated as an empty registry.
func Scan(root string) ([]Entry, []error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		if os.IsPermission(err) {
			return nil, []error{errs.Wrap(errs.Permission, err, "cannot read models_root %s", root).
				WithHint("check directory permissions, or run with sudo")}
		}
		return nil, []error{errs.Wrap(errs.IO, err, "cannot read models_root %s", root)}
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
			// Directories without maio.toml are not models.
			if errs.IsCode(err, errs.NotFound) && !hasToml(dir) {
				continue
			}
			warns = append(warns, fmt.Errorf("%s: %w", dir, err))
			out = append(out, Entry{Model: &config.Model{Name: e.Name(), Dir: dir}, Err: err, Missing: true})
			continue
		}
		var size int64
		missing := false
		if st, statErr := os.Stat(m.FilePath()); statErr == nil {
			size = st.Size()
		} else {
			missing = true
			warns = append(warns, fmt.Errorf("%s: model file is missing: %s", dir, m.FilePath()))
		}
		out = append(out, Entry{Model: m, Size: size, Missing: missing})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model.Name < out[j].Model.Name })
	return out, warns
}

func hasToml(dir string) bool {
	_, err := os.Stat(config.ModelConfigPath(dir))
	return err == nil
}

// Find looks up a model by name (normalized automatically). It reads
// maio.toml; the model file itself is checked by the caller.
func Find(root, rawName string) (*config.Model, error) {
	name, err := naming.Normalize(rawName)
	if err != nil {
		return nil, err
	}
	dir := config.ModelDir(root, name)
	m, err := config.LoadModel(dir)
	if err != nil {
		if errs.IsCode(err, errs.NotFound) {
			return nil, errs.Newf(errs.NotFound, "model %s is not installed", name).
				WithHint("run maio list; install it with: sudo maio install %s", name)
		}
		return nil, err
	}
	return m, nil
}
