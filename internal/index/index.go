// Package index reads the model index (name -> direct download URL).
//
//	"deepseek-v4.1" = "https://example.com/deepseek-v4.1-Q4_K_M.gguf"
package index

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/toml"
)

// Index maps a model name to its direct download URL.
type Index struct {
	urls map[string]string
}

// PathCandidates returns the lookup order for index files.
func PathCandidates() []string {
	var out []string
	if p := os.Getenv("MAIO_INDEX"); p != "" {
		out = append(out, p)
	}
	if wd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(wd, "index.toml"))
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "index.toml"))
	}
	out = append(out, config.IndexPath())
	return out
}

// Load finds the first usable index in PathCandidates order; otherwise returns an empty index.
func Load() (*Index, error) {
	for _, p := range PathCandidates() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		doc, err := toml.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse index %s: %w", p, err)
		}
		idx := &Index{urls: make(map[string]string, len(doc.Values))}
		for k, v := range doc.Values {
			if s, ok := v.(string); ok {
				idx.urls[k] = s
			}
		}
		return idx, nil
	}
	return &Index{urls: map[string]string{}}, nil
}

// URL looks up the direct URL for a name.
func (i *Index) URL(name string) (string, bool) {
	u, ok := i.urls[name]
	return u, ok
}
