// Package plugin installs and manages maio plugins.
//
// A plugin is a GitHub repository with an optional plugin.json at its root.
// Installation downloads the repository tarball, validates the manifest, and
// unpacks it under <data_dir>/plugins/<name>. No plugin code is executed by
// this package.
package plugin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/market"
)

const (
	maxArchiveBytes = 64 << 20
	maxExtractBytes = 128 << 20
	maxFiles        = 2000
	metaFile        = ".maio-plugin.json"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// Manifest is the optional plugin.json contract.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Homepage    string `json:"homepage,omitempty"`
	Main        string `json:"main,omitempty"`
	Web         struct {
		Panel string `json:"panel,omitempty"`
	} `json:"web,omitempty"`
}

// Installed is the on-disk record of one plugin.
type Installed struct {
	Manifest
	Repo        string    `json:"repo"`
	Branch      string    `json:"branch"`
	ArchiveSHA  string    `json:"archive_sha256"`
	InstalledAt time.Time `json:"installed_at"`
	Dir         string    `json:"dir"`
}

// Manager owns one plugins directory.
type Manager struct {
	Dir          string
	HTTP         *http.Client
	CodeloadBase string
}

// NewManager builds a manager rooted at <data_dir>/plugins.
func NewManager(dataDir string) *Manager {
	return &Manager{
		Dir: filepath.Join(dataDir, "plugins"),
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     false,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
				if conn, err := d.DialContext(ctx, "tcp4", addr); err == nil {
					return conn, nil
				}
				return d.DialContext(ctx, "tcp", addr)
			},
		}},
		CodeloadBase: "https://codeload.github.com",
	}
}

// ValidateName checks the plugin directory name.
func ValidateName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !validName.MatchString(name) {
		return "", errs.New(errs.Usage, "invalid plugin name %q", name).
			WithHint("allowed characters: a-z 0-9 - _ and the name must start with a letter or digit")
	}
	return name, nil
}

// List returns installed plugins and recoverable problems.
func (m *Manager) List() ([]Installed, []error) {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{errs.Wrap(errs.Permission, err, "cannot list %s", m.Dir)}
	}
	var out []Installed
	var problems []error
	for _, de := range entries {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		inst, err := m.readMeta(de.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, *inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// Get returns one installed plugin.
func (m *Manager) Get(name string) (*Installed, error) {
	name, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	return m.readMeta(name)
}

func (m *Manager) readMeta(name string) (*Installed, error) {
	dir := filepath.Join(m.Dir, name)
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.New(errs.NotFound, "plugin %q is not installed", name).
				WithHint("run \"maio install %s\"", name)
		}
		return nil, errs.Wrap(errs.Permission, err, "cannot read plugin metadata")
	}
	var inst Installed
	if err := json.Unmarshal(raw, &inst); err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid plugin metadata in %s", dir)
	}
	if inst.Name == "" {
		inst.Name = name
	}
	inst.Dir = dir
	return &inst, nil
}

// Install fetches one market entry.
func (m *Manager) Install(ctx context.Context, entry market.Plugin, force bool) (*Installed, error) {
	branch := entry.Branch
	if branch == "" {
		branch = "main"
	}
	return m.InstallRepo(ctx, entry.Name, entry.Repo, branch, entry.Author, entry.Description, force)
}

// InstallURL installs a plugin directly from a GitHub URL.
func (m *Manager) InstallURL(ctx context.Context, repo, name string, force bool) (*Installed, error) {
	if name == "" {
		_, base, err := repoParts(repo)
		if err != nil {
			return nil, err
		}
		name = base
	}
	return m.InstallRepo(ctx, name, repo, "main", "", "", force)
}

// InstallRepo downloads, validates and unpacks one repository.
func (m *Manager) InstallRepo(ctx context.Context, name, repo, branch, author, description string, force bool) (*Installed, error) {
	name, err := ValidateName(name)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = "main"
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create %s", m.Dir)
	}
	dest := filepath.Join(m.Dir, name)
	if _, err := os.Stat(dest); err == nil && !force {
		return nil, errs.New(errs.Conflict, "plugin %q is already installed", name).
			WithHint("run \"maio plugins update %s\" or pass --force", name)
	}
	stage, err := os.MkdirTemp(m.Dir, ".stage-"+name+"-")
	if err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create staging directory")
	}
	renamed := false
	defer func() {
		if !renamed {
			os.RemoveAll(stage)
		}
	}()

	archive, sum, err := m.download(ctx, repo, branch)
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive)

	if err := extractArchive(archive, stage); err != nil {
		return nil, err
	}
	manifest, err := readManifest(stage, name, author, description)
	if err != nil {
		return nil, err
	}
	inst := Installed{
		Manifest: manifest, Repo: repo, Branch: branch, ArchiveSHA: sum,
		InstalledAt: time.Now().UTC(), Dir: dest,
	}
	meta, err := json.MarshalIndent(inst, "", "  ")
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err, "cannot encode plugin metadata")
	}
	if err := os.WriteFile(filepath.Join(stage, metaFile), append(meta, '\n'), 0o644); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot write plugin metadata")
	}

	var old string
	if _, err := os.Stat(dest); err == nil {
		old = dest + ".old-" + fmt.Sprint(time.Now().UnixNano())
		if err := os.Rename(dest, old); err != nil {
			return nil, errs.Wrap(errs.Permission, err, "cannot replace %s", dest)
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		if old != "" {
			_ = os.Rename(old, dest)
		}
		return nil, errs.Wrap(errs.Permission, err, "cannot move plugin into %s", dest)
	}
	renamed = true
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return &inst, nil
}

// Remove deletes one installed plugin directory.
func (m *Manager) Remove(name string) error {
	name, err := ValidateName(name)
	if err != nil {
		return err
	}
	dir := filepath.Join(m.Dir, name)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return errs.New(errs.NotFound, "plugin %q is not installed", name)
		}
		return errs.Wrap(errs.Permission, err, "cannot stat %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot remove %s", dir)
	}
	return nil
}

// Update re-downloads an installed plugin from its recorded repository.
func (m *Manager) Update(ctx context.Context, name string) (*Installed, error) {
	cur, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if cur.Repo == "" {
		return nil, errs.New(errs.Config, "plugin %s has no recorded repository", name)
	}
	return m.InstallRepo(ctx, cur.Name, cur.Repo, cur.Branch, cur.Author, cur.Description, true)
}

// download saves the repository tarball to a temporary file and returns its path and digest.
func (m *Manager) download(ctx context.Context, repo, branch string) (string, string, error) {
	owner, name, err := repoParts(repo)
	if err != nil {
		return "", "", err
	}
	urls := []string{
		fmt.Sprintf("%s/%s/%s/tar.gz/refs/heads/%s", strings.TrimRight(m.CodeloadBase, "/"), owner, name, url.PathEscape(branch)),
	}
	if branch == "main" {
		urls = append(urls, fmt.Sprintf("%s/%s/%s/tar.gz/refs/heads/master", strings.TrimRight(m.CodeloadBase, "/"), owner, name))
	}
	var last error
	for _, u := range urls {
		f, sum, err := m.fetchArchive(ctx, u)
		if err == nil {
			return f, sum, nil
		}
		last = err
	}
	return "", "", last
}

func (m *Manager) fetchArchive(ctx context.Context, u string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", errs.Wrap(errs.Usage, err, "invalid plugin archive URL")
	}
	req.Header.Set("User-Agent", "maio-plugin/1")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return "", "", errs.Wrap(errs.Network, err, "cannot download plugin archive")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		if resp.StatusCode == http.StatusNotFound {
			return "", "", errs.New(errs.NotFound, "plugin repository or branch not found: %s", u).
				WithHint("check the repo URL and branch in plugins.json")
		}
		return "", "", errs.New(errs.Unavailable, "plugin archive returned HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "maio-plugin-*.tar.gz")
	if err != nil {
		return "", "", errs.Wrap(errs.Permission, err, "cannot create temporary file")
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxArchiveBytes+1))
	closeErr := tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return "", "", errs.Wrap(errs.Network, err, "cannot download plugin archive")
	}
	if closeErr != nil {
		os.Remove(tmp.Name())
		return "", "", errs.Wrap(errs.Permission, closeErr, "cannot write plugin archive")
	}
	if n > maxArchiveBytes {
		os.Remove(tmp.Name())
		return "", "", errs.New(errs.Config, "plugin archive is larger than %d MiB", maxArchiveBytes>>20)
	}
	return tmp.Name(), hex.EncodeToString(hasher.Sum(nil)), nil
}

func repoParts(repo string) (string, string, error) {
	repo = strings.TrimSuffix(strings.TrimSpace(repo), "/")
	u, err := url.Parse(repo)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", "", errs.New(errs.Usage, "plugin repo must be https://github.com/<owner>/<repo>, got %q", repo)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", errs.New(errs.Usage, "plugin repo must contain exactly owner/repo: %q", repo)
	}
	owner := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	if owner == "" || name == "" || strings.ContainsAny(owner+name, " \t") {
		return "", "", errs.New(errs.Usage, "invalid plugin repo %q", repo)
	}
	return owner, name, nil
}

func extractArchive(file, dst string) error {
	f, err := os.Open(file)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot open plugin archive")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errs.Wrap(errs.Integrity, err, "plugin archive is not gzip")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errs.Wrap(errs.Integrity, err, "plugin archive is corrupt")
		}
		clean := path.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return errs.New(errs.Integrity, "plugin archive contains an unsafe path %q", hdr.Name)
		}
		parts := strings.Split(clean, "/")
		if len(parts) < 2 {
			continue // the top-level repo directory itself
		}
		rel := path.Join(parts[1:]...)
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		relCheck, err := filepath.Rel(dst, target)
		if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
			return errs.New(errs.Integrity, "plugin archive escapes the staging directory")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return errs.Wrap(errs.Permission, err, "cannot create plugin directory")
			}
		case tar.TypeReg:
			files++
			if files > maxFiles {
				return errs.New(errs.Config, "plugin archive has more than %d files", maxFiles)
			}
			total += hdr.Size
			if total > maxExtractBytes {
				return errs.New(errs.Config, "plugin archive expands beyond %d MiB", maxExtractBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return errs.Wrap(errs.Permission, err, "cannot create plugin directory")
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return errs.Wrap(errs.Permission, err, "cannot write plugin file")
			}
			n, err := io.Copy(out, io.LimitReader(tr, hdr.Size))
			closeErr := out.Close()
			if err != nil || closeErr != nil || n != hdr.Size {
				return errs.New(errs.Integrity, "plugin file %q is truncated", rel)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return errs.New(errs.Integrity, "plugin archive contains a link (%q); links are not allowed", rel)
		default:
			// Devices, fifos and other special entries are ignored.
		}
	}
	return nil
}

func readManifest(dir, name, author, description string) (Manifest, error) {
	m := Manifest{Name: name, Version: "0.0.0", Author: author, Description: description}
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return Manifest{}, errs.Wrap(errs.Config, err, "plugin.json is not valid JSON")
		}
		if m.Name == "" {
			m.Name = name
		}
		if strings.ToLower(m.Name) != name {
			return Manifest{}, errs.New(errs.Config, "plugin.json name %q does not match market name %q", m.Name, name)
		}
		if m.Version == "" {
			m.Version = "0.0.0"
		}
		if m.Author == "" {
			m.Author = author
		}
		if m.Description == "" {
			m.Description = description
		}
	} else if !os.IsNotExist(err) {
		return Manifest{}, errs.Wrap(errs.Permission, err, "cannot read plugin.json")
	}
	return m, nil
}
