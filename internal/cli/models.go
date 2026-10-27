package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
	"github.com/ShieldDog-ACE/maio-cluster/internal/naming"
	"github.com/ShieldDog-ACE/maio-cluster/internal/registry"
)

// cmdCp duplicates a model locally, preferring a hardlink so a 15 GiB model
// costs no extra disk space or time.
func cmdCp(conf *config.Global, args []string) int {
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio cp <source> <destination>")
			return 0
		default:
			if strings.HasPrefix(a, "-") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return usagef("usage: maio cp <source> <destination>")
	}
	srcName, err := naming.Normalize(pos[0])
	if err != nil {
		return fail(err)
	}
	dstName, err := naming.Normalize(pos[1])
	if err != nil {
		return fail(err)
	}
	if srcName == dstName {
		return usagef("source and destination are the same model")
	}
	src, err := registry.Find(conf.ModelsRoot, srcName)
	if err != nil {
		return fail(err)
	}
	dstDir := config.ModelDir(conf.ModelsRoot, dstName)
	if _, err := os.Stat(config.ModelConfigPath(dstDir)); err == nil {
		return fail(errs.Newf(errs.Conflict, "model %s already exists in %s", dstName, dstDir).
			WithHint("choose another destination, or remove the existing model first"))
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fail(errs.Wrap(errs.Permission, err, "cannot create %s", dstDir))
	}

	modelRel := ""
	mode := ""
	err = filepath.Walk(src.Dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if path == dstDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Name() == "maio.toml" {
			return nil
		}
		rel, rerr := filepath.Rel(src.Dir, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		m, cerr := linkOrCopy(path, target, info.Mode().Perm())
		if cerr != nil {
			return cerr
		}
		if mode == "" {
			mode = m
		}
		if path == src.FilePath() {
			modelRel = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(dstDir)
		e := errs.Wrap(errs.IO, err, "cannot copy %s to %s", srcName, dstName)
		e.Hint = "check free space and permissions; cross-device copies fall back to a full copy"
		return fail(e)
	}
	if modelRel == "" {
		base := filepath.Base(src.FilePath())
		if err := func() error {
			m, cerr := linkOrCopy(src.FilePath(), filepath.Join(dstDir, base), 0o644)
			if cerr != nil {
				return cerr
			}
			if mode == "" {
				mode = m
			}
			return nil
		}(); err != nil {
			_ = os.RemoveAll(dstDir)
			return fail(errs.Wrap(errs.IO, err, "cannot copy the model file %s", src.FilePath()))
		}
		modelRel = base
	}
	if err := config.SaveModel(dstDir, dstName, "gguf", modelRel); err != nil {
		_ = os.RemoveAll(dstDir)
		return fail(err)
	}
	fmt.Printf("copied %s -> %s (%s)\n", srcName, dstName, mode)
	logging.Info("model.cp", "source", srcName, "dest", dstName, "mode", mode, "dir", dstDir)
	return 0
}

func linkOrCopy(src, dst string, mode os.FileMode) (string, error) {
	if err := os.Link(src, dst); err == nil {
		return "hardlink", nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", err
	}
	return "copy", nil
}

// cmdLoad asks the running service to load and pin a model.
func cmdLoad(conf *config.Global, args []string) int {
	var pos []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: maio load <name>")
			fmt.Println("       pins the model; it is restored after a service restart")
			return 0
		default:
			if strings.HasPrefix(a, "-") {
				return usagef("unknown argument %s", a)
			}
			pos = append(pos, a)
		}
	}
	if len(pos) != 1 {
		return usagef("usage: maio load <name>")
	}
	name, err := naming.Normalize(pos[0])
	if err != nil {
		return fail(err)
	}
	client := newAPIClient(conf)
	ctx, cancel := contextLong()
	defer cancel()
	var out map[string]any
	if err := client.post(ctx, "/api/load", map[string]string{"model": name}, &out); err != nil {
		if ae, ok := err.(*apiError); ok && ae.Status == 404 {
			return fail(errs.Newf(errs.NotFound, "model %s is not installed", name))
		}
		return printAPIError(err)
	}
	fmt.Printf("loaded and pinned %s\n", name)
	logging.Info("model.load", "model", name)
	return 0
}
