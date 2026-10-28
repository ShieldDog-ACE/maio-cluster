// Package update checks and applies maio CLI releases.
//
// It is deliberately small: one GitHub release lookup, one asset download,
// one SHA-256 check, one atomic binary replace. Models and plugins are handled
// by the market/plugin packages instead.
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
)

const (
	apiBase     = "https://api.github.com/repos/"
	defaultRepo = "ShieldDog-ACE/maio-cluster"
	maxBinary   = 64 << 20
	maxChecksum = 4 << 10
	httpTimeout = 45 * time.Second
)

// Asset is one release artifact.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the subset of the GitHub release API we need.
type Release struct {
	Tag    string  `json:"tag_name"`
	Name   string  `json:"name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Version strips a leading v.
func Version(tag string) string { return strings.TrimPrefix(strings.TrimSpace(tag), "v") }

// Compare returns -1, 0 or 1 for dotted numeric versions. Pre-release
// suffixes are ignored for ordering; a dev build is treated as its base.
func Compare(a, b string) int {
	pa, pb := numbers(a), numbers(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func numbers(v string) []int {
	v = Version(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(part)
		out = append(out, n)
	}
	return out
}

// NewClient builds a client that prefers IPv4, like the market client.
func NewClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     false,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
					return conn, nil
				}
				return dialer.DialContext(ctx, "tcp", addr)
			},
		},
	}
}

// Latest fetches the latest published release.
func Latest(ctx context.Context, client *http.Client, repo string) (*Release, error) {
	if repo == "" {
		repo = defaultRepo
	}
	if client == nil {
		client = NewClient()
	}
	url := apiBase + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errs.Wrap(errs.Usage, err, "invalid release URL")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "maio-update/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.Network, err, "cannot check the latest release").
			WithHint("check the network, or pass --version")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, errs.New(errs.Unavailable, "release API returned HTTP %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid release response")
	}
	if rel.Tag == "" {
		return nil, errs.New(errs.Unavailable, "release response has no tag")
	}
	return &rel, nil
}

// AssetName returns the release artifact name for this platform.
func AssetName(goos, arch string) string {
	switch goos {
	case "linux":
		return "maio-linux-" + arch + ".tar.gz"
	case "darwin":
		return "maio-mac-" + arch + ".tar.gz"
	case "windows":
		return "maio-windows-amd64.zip"
	default:
		return ""
	}
}

// Find returns the named asset.
func (r *Release) Find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Download fetches one asset into dir and verifies its .sha256 sibling when
// present. It returns the local path and the digest.
func Download(ctx context.Context, client *http.Client, rel *Release, asset Asset, dir string) (string, string, error) {
	if client == nil {
		client = NewClient()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", errs.Wrap(errs.Permission, err, "cannot create %s", dir)
	}
	target := filepath.Join(dir, asset.Name)
	if err := fetch(ctx, client, asset.URL, target); err != nil {
		return "", "", err
	}
	sum, err := fileSHA256(target)
	if err != nil {
		return "", "", err
	}
	if sumAsset, ok := rel.Find(asset.Name + ".sha256"); ok {
		raw, err := fetchBytes(ctx, client, sumAsset.URL, maxChecksum)
		if err != nil {
			return "", "", err
		}
		want := strings.Fields(string(raw))
		if len(want) == 0 {
			return "", "", errs.New(errs.Integrity, "empty checksum asset for %s", asset.Name)
		}
		if !strings.EqualFold(want[0], sum) {
			return "", "", errs.New(errs.Integrity, "SHA-256 mismatch for %s", asset.Name).
				WithHint("expected %s, got %s", want[0], sum)
		}
	}
	return target, sum, nil
}

// ExtractBinary pulls maio (or maio.exe) out of a release archive.
func ExtractBinary(archive, goos, dest string) error {
	if goos == "windows" {
		return extractZip(archive, dest)
	}
	return extractTarGz(archive, dest)
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot open update archive")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errs.Wrap(errs.Integrity, err, "update archive is not gzip")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errs.Wrap(errs.Integrity, err, "update archive is corrupt")
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != "maio" {
			continue
		}
		if hdr.Size > maxBinary {
			return errs.New(errs.Config, "maio binary is too large")
		}
		return writeLimited(dest, tr, hdr.Size)
	}
	return errs.New(errs.NotFound, "update archive does not contain maio")
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return errs.Wrap(errs.Integrity, err, "update archive is not a zip")
	}
	defer zr.Close()
	for _, f := range zr.File {
		if path.Base(f.Name) != "maio" && path.Base(f.Name) != "maio.exe" {
			continue
		}
		if f.UncompressedSize64 > maxBinary {
			return errs.New(errs.Config, "maio binary is too large")
		}
		rc, err := f.Open()
		if err != nil {
			return errs.Wrap(errs.Integrity, err, "cannot read maio.exe")
		}
		defer rc.Close()
		return writeLimited(dest, rc, int64(f.UncompressedSize64))
	}
	return errs.New(errs.NotFound, "update archive does not contain maio.exe")
}

func writeLimited(dest string, r io.Reader, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create update directory")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write update binary")
	}
	n, err := io.Copy(f, io.LimitReader(r, size+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != size {
		return errs.New(errs.Integrity, "update binary is truncated")
	}
	return nil
}

// ReplaceSelf atomically swaps the running binary and returns the real path.
func ReplaceSelf(newBinary string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot locate the running binary")
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	dir := filepath.Dir(real)
	tmp, err := os.CreateTemp(dir, ".maio-update-")
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot write next to %s", real).
			WithHint("run with sudo for a system install")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	src, err := os.Open(newBinary)
	if err != nil {
		tmp.Close()
		return "", errs.Wrap(errs.Permission, err, "cannot read downloaded binary")
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", errs.Wrap(errs.Permission, err, "cannot chmod the new binary")
	}
	if closeErr := tmp.Close(); copyErr != nil || closeErr != nil {
		return "", errs.New(errs.Integrity, "cannot write the new binary")
	}
	backup := real + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(real, backup); err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot replace %s", real).
			WithHint("run with sudo for a system install")
	}
	if err := os.Rename(tmpName, real); err != nil {
		_ = os.Rename(backup, real)
		return "", errs.Wrap(errs.Permission, err, "cannot install the new binary")
	}
	_ = os.Remove(backup)
	return real, nil
}

// APTOwned reports whether dpkg owns the running binary.
func APTOwned(exe string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("dpkg-query", "-S", exe).CombinedOutput()
	return err == nil && strings.Contains(string(out), "maio-cluster")
}

// SystemPath reports whether the binary lives in a machine-wide prefix.
func SystemPath(exe string) bool {
	for _, prefix := range []string{"/usr/", "/opt/", "/Library/"} {
		if strings.HasPrefix(exe, prefix) {
			return true
		}
	}
	return false
}

func fetch(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errs.Wrap(errs.Usage, err, "invalid download URL")
	}
	req.Header.Set("User-Agent", "maio-update/1")
	resp, err := client.Do(req)
	if err != nil {
		return errs.Wrap(errs.Network, err, "download failed: %s", url)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return errs.New(errs.Network, "download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot write %s", dest)
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxBinary+1<<20))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return errs.New(errs.Network, "download interrupted")
	}
	return nil
}

func fetchBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errs.Wrap(errs.Usage, err, "invalid URL")
	}
	req.Header.Set("User-Agent", "maio-update/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.Network, err, "download failed: %s", url)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errs.New(errs.Network, "download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errs.Wrap(errs.Network, err, "cannot read checksum")
	}
	if int64(len(data)) > limit {
		return nil, errs.New(errs.Integrity, "checksum response is too large")
	}
	return data, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot read update archive")
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot hash update archive")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Describe renders a short progress line.
func Describe(rel *Release) string {
	return fmt.Sprintf("%s (%s)", Version(rel.Tag), rel.URL)
}
