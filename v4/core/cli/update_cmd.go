package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/update"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// cmdUpdate updates the maio CLI itself. Models and plugins have their own
// commands (pull/install); this is the third leg of the package manager.
func (o *options) cmdUpdate(args []string) int {
	check := false
	force := false
	repo := "ShieldDog-ACE/maio-cluster"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		case "--force", "-f":
			force = true
		case "--repo":
			if i+1 >= len(args) {
				return usageMsg("--repo requires owner/name")
			}
			i++
			repo = args[i]
		case "--help", "-h":
			fmt.Println("usage: maio update [--check] [--force] [--repo owner/name]")
			fmt.Println("       updates the maio CLI from GitHub releases")
			fmt.Println("       models: maio pull <name>   plugins: maio install <name>")
			return 0
		default:
			return usageMsg("unknown argument " + args[i])
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := update.NewClient()
	rel, err := update.Latest(ctx, client, repo)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	latest := update.Version(rel.Tag)
	current := version.Version
	fmt.Printf("current: %s\nlatest:  %s\n", current, latest)
	if check {
		return 0
	}

	exe, _ := os.Executable()
	real := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		real = r
	}
	if !force && update.APTOwned(real) {
		fmt.Println("\nmaio was installed by apt; use the package manager:")
		fmt.Println("  sudo apt update && sudo apt install --only-upgrade maio-cluster")
		fmt.Println("or force a direct binary replace: maio update --force")
		return 0
	}
	dev := strings.Contains(current, "-dev")
	cmp := update.Compare(latest, current)
	if !force && cmp < 0 {
		fmt.Println("current build is newer than the latest release")
		return 0
	}
	if !force && cmp == 0 && !dev {
		fmt.Println("already up to date")
		return 0
	}

	assetName := update.AssetName(runtime.GOOS, runtime.GOARCH)
	if assetName == "" {
		errs.Print(os.Stderr, errs.New(errs.Usage, "no update artifact for %s/%s", runtime.GOOS, runtime.GOARCH))
		return 2
	}
	asset, ok := rel.Find(assetName)
	if !ok {
		errs.Print(os.Stderr, errs.New(errs.NotFound, "release %s has no %s", rel.Tag, assetName))
		return 4
	}
	tmp, err := os.MkdirTemp("", "maio-update-")
	if err != nil {
		errs.Print(os.Stderr, errs.Wrap(errs.Permission, err, "cannot create temporary directory"))
		return 5
	}
	defer os.RemoveAll(tmp)
	fmt.Printf("downloading %s (%s)\n", asset.Name, humanSize(asset.Size))
	archive, sum, err := update.Download(ctx, client, rel, asset, tmp)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("sha256 %s\n", sum)
	newBin := tmp + "/maio.new"
	if runtime.GOOS == "windows" {
		newBin += ".exe"
	}
	if err := update.ExtractBinary(archive, runtime.GOOS, newBin); err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	path, err := update.ReplaceSelf(newBin)
	if err != nil {
		errs.Print(os.Stderr, err)
		return errs.ExitCode(err)
	}
	fmt.Printf("updated %s to %s\n", path, latest)
	fmt.Println("restart the service to use the new binary:")
	fmt.Println("  systemctl --user restart maio    # user install")
	fmt.Println("  sudo systemctl restart maio      # system install")
	return 0
}
