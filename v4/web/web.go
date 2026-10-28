// Package webui embeds the optional maio web console.
//
// The console is not served unless the user explicitly installs it with
// "maio web install". The binary still carries the assets so an air-gapped
// machine can install the console without another download.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

//go:embed assets
var embedded embed.FS

// Dir returns <data_dir>/web.
func Dir(dataDir string) string { return filepath.Join(dataDir, "web") }

// AssetFS returns the embedded console rooted at index.html.
func AssetFS() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}

// Installed reports whether index.html exists in the data directory.
func Installed(dataDir string) bool {
	st, err := os.Stat(filepath.Join(Dir(dataDir), "index.html"))
	return err == nil && !st.IsDir()
}

// Install materializes the embedded console under <data_dir>/web.
func Install(dataDir string, force bool) (string, error) {
	dir := Dir(dataDir)
	if Installed(dataDir) && !force {
		return dir, errs.New(errs.Conflict, "web console is already installed at %s", dir).
			WithHint("run \"maio web install --force\" to replace it")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot create %s", dataDir)
	}
	stage, err := os.MkdirTemp(dataDir, ".web-stage-")
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot create staging directory")
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stage)
		}
	}()
	if err := copyFS(AssetFS(), stage); err != nil {
		return "", err
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"component":    "maio-web",
		"version":      version.Version,
		"installed_at": time.Now().UTC(),
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(stage, ".maio-web.json"), append(meta, '\n'), 0o644); err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot write web metadata")
	}

	var old string
	if _, err := os.Stat(dir); err == nil {
		old = dir + ".old-" + fmt.Sprint(time.Now().UnixNano())
		if err := os.Rename(dir, old); err != nil {
			return "", errs.Wrap(errs.Permission, err, "cannot replace %s", dir)
		}
	}
	if err := os.Rename(stage, dir); err != nil {
		if old != "" {
			_ = os.Rename(old, dir)
		}
		return "", errs.Wrap(errs.Permission, err, "cannot move web console into %s", dir)
	}
	cleanup = false
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return dir, nil
}

// Remove deletes the installed console.
func Remove(dataDir string) error {
	dir := Dir(dataDir)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return errs.New(errs.NotFound, "web console is not installed")
		}
		return errs.Wrap(errs.Permission, err, "cannot stat %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot remove %s", dir)
	}
	return nil
}

func copyFS(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return errs.Wrap(errs.Permission, err, "cannot create %s", target)
			}
			return nil
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return errs.Wrap(errs.Internal, err, "cannot read embedded %s", p)
		}
		mode := os.FileMode(0o644)
		if info, err := d.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return errs.Wrap(errs.Permission, err, "cannot write %s", target)
		}
		return nil
	})
}
