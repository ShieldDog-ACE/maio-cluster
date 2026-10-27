// Package model owns the on-disk model cache shared by every maio platform.
//
// One model is one directory containing a maio.toml and one GGUF file. The
// toml is authoritative; the runtime state never gets written into it.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/gguf"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/naming"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/toml"
)

// Entry is one installed model.
type Entry struct {
	Name    string         `json:"name"`
	Format  string         `json:"format"`
	Path    string         `json:"path"`
	AbsPath string         `json:"abs_path"`
	Dir     string         `json:"dir"`
	Size    int64          `json:"size_bytes"`
	Meta    *gguf.Metadata `json:"gguf,omitempty"`
}

// Store is rooted at ModelsDir.
type Store struct {
	Root   string
	Client *http.Client
	// Token is sent as a bearer token only to TokenHost (the coordinator).
	// It must never leak to an external model mirror.
	Token      string
	TokenHosts []string
}

// NewStore creates a model store. The root is created lazily on write.
func NewStore(root string) *Store {
	return &Store{
		Root: root,
		Client: &http.Client{
			Timeout: 0, // downloads are bounded by context and progress, not a total timeout
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
	}
}

// Dir returns the model directory.
func (s *Store) Dir(name string) string { return filepath.Join(s.Root, name) }

// ConfigPath returns <dir>/maio.toml.
func (s *Store) ConfigPath(name string) string { return filepath.Join(s.Dir(name), "maio.toml") }

// List returns every valid model and a list of recoverable problems.
func (s *Store) List() ([]Entry, []error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{errs.Wrap(errs.Permission, err, "cannot list models in %s", s.Root)}
	}
	var out []Entry
	var problems []error
	for _, de := range entries {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		e, err := s.Get(de.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, *e)
	}
	return out, problems
}

// Get reads one model entry.
func (s *Store) Get(name string) (*Entry, error) {
	n, err := naming.Normalize(name)
	if err != nil {
		return nil, err
	}
	dir := s.Dir(n)
	cfgPath := filepath.Join(dir, "maio.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.New(errs.NotFound, "model %s is not installed in %s", n, s.Root).
				WithHint("run \"maio model install %s --url <path-or-url>\"", n)
		}
		return nil, errs.Wrap(errs.Permission, err, "cannot read %s", cfgPath)
	}
	doc, err := toml.Parse(data)
	if err != nil {
		return nil, errs.Wrap(errs.Config, err, "invalid %s", cfgPath)
	}
	for _, key := range doc.Keys() {
		switch key {
		case "name", "format", "path":
		default:
			return nil, errs.New(errs.Config, "%s:%d: unknown key %q", cfgPath, doc.Line(key), key)
		}
	}
	cfgName, ok := doc.String("name")
	if !ok {
		return nil, errs.New(errs.Config, "%s: name must be a string", cfgPath)
	}
	format, ok := doc.String("format")
	if !ok || format != "gguf" {
		return nil, errs.New(errs.Config, "%s: format must be \"gguf\"", cfgPath)
	}
	rel, ok := doc.String("path")
	if !ok || strings.TrimSpace(rel) == "" {
		return nil, errs.New(errs.Config, "%s: path must be a non-empty string", cfgPath)
	}
	if cfgName != n {
		return nil, errs.New(errs.Config, "%s: name %q does not match directory %q", cfgPath, cfgName, n)
	}
	abs := rel
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, filepath.FromSlash(rel))
	}
	abs = filepath.Clean(abs)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, errs.Wrap(errs.Config, err, "cannot resolve model directory %s", dir)
	}
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, errs.New(errs.NotFound, "model file is missing: %s", abs).
			WithHint("re-download with \"maio model install %s --url <url>\"", n)
	}
	relCheck, err := filepath.Rel(realDir, realAbs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return nil, errs.New(errs.Config, "model path escapes its directory: %s", abs)
	}
	st, err := os.Stat(realAbs)
	if err != nil {
		return nil, errs.Wrap(errs.NotFound, err, "cannot stat model file")
	}
	meta, _ := gguf.ParseFile(realAbs)
	return &Entry{
		Name: n, Format: format, Path: rel, AbsPath: realAbs, Dir: dir,
		Size: st.Size(), Meta: meta,
	}, nil
}

// Has reports whether a model is installed and readable.
func (s *Store) Has(name string) bool {
	_, err := s.Get(name)
	return err == nil
}

// Install adds a model from a local path or http(s) URL. When sha256 is not
// empty the source is verified before the model becomes visible.
func (s *Store) Install(name, source, wantSHA string, progress func(done, total int64)) (*Entry, error) {
	n, err := naming.Normalize(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.ConfigPath(n)); err == nil {
		return nil, errs.New(errs.Conflict, "model %s is already installed in %s", n, s.Dir(n)).
			WithHint("remove it first with \"maio model rm %s\"", n)
	}
	if err := os.MkdirAll(s.Dir(n), 0o755); err != nil {
		return nil, errs.Wrap(errs.Permission, err, "cannot create %s", s.Dir(n))
	}
	var final string
	if isRemote(source) {
		final, err = s.download(n, source, wantSHA, progress)
	} else {
		final, err = s.addLocal(n, source, wantSHA, progress)
	}
	if err != nil {
		// Only remove the directory when this call created it and no config
		// was written. A concurrent installer is protected by the config check.
		if _, statErr := os.Stat(s.ConfigPath(n)); os.IsNotExist(statErr) {
			os.RemoveAll(s.Dir(n))
		}
		return nil, err
	}
	cfg := fmt.Sprintf("name   = %q\nformat = %q\npath   = %q\n", n, "gguf", filepath.Base(final))
	if err := writeFileAtomic(s.ConfigPath(n), []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	return s.Get(n)
}

func (s *Store) addLocal(name, src, wantSHA string, progress func(int64, int64)) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", errs.Wrap(errs.Usage, err, "invalid model path %q", src)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", errs.Wrap(errs.NotFound, err, "model file not found: %s", abs)
	}
	if st.IsDir() {
		return "", errs.New(errs.Usage, "model source is a directory: %s", abs)
	}
	if wantSHA != "" {
		sum, err := fileSHA256(abs)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(sum, wantSHA) {
			return "", errs.New(errs.Integrity, "SHA-256 mismatch for %s", abs).
				WithHint("expected %s, got %s", wantSHA, sum)
		}
	}
	if _, err := gguf.ParseFile(abs); err != nil {
		return "", err
	}
	dst := filepath.Join(s.Dir(name), filepath.Base(abs))
	if same, _ := sameFile(abs, dst); same {
		return dst, nil
	}
	if st2, err := os.Stat(dst); err == nil && st2.Size() == st.Size() {
		// A previous interrupted install already produced the file.
		if m, err := gguf.ParseFile(dst); err == nil {
			_ = m
			return dst, nil
		}
	}
	if err := linkOrCopy(abs, dst, st, progress); err != nil {
		return "", err
	}
	return dst, nil
}

func (s *Store) download(name, rawURL, wantSHA string, progress func(int64, int64)) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errs.New(errs.Usage, "unsupported download URL %q", rawURL).
			WithHint("use http://, https://, or a local GGUF path")
	}
	base := filepath.Base(u.Path)
	if !strings.HasSuffix(strings.ToLower(base), ".gguf") {
		base = name + ".gguf"
	}
	dst := filepath.Join(s.Dir(name), base)
	part := dst + ".part"

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errs.Wrap(errs.Network, err, "cannot build download request")
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	if s.Token != "" && s.tokenAllowed(u) {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.Network, err, "download failed: %s", rawURL).
			WithHint("check the URL, the LAN link, and the proxy settings")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		_ = os.Remove(part)
	case http.StatusPartialContent:
		if offset == 0 {
			return "", errs.New(errs.Network, "server returned 206 without a range request")
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// The partial file is already complete; verify it below.
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", errs.New(errs.Network, "download denied (HTTP %d): %s", resp.StatusCode, rawURL).
			WithHint("the URL may require a token or may have expired")
	case http.StatusNotFound:
		return "", errs.New(errs.NotFound, "download not found (HTTP 404): %s", rawURL)
	default:
		return "", errs.New(errs.Network, "download failed (HTTP %d): %s", resp.StatusCode, rawURL)
	}
	total := resp.ContentLength
	if total > 0 && resp.StatusCode == http.StatusPartialContent {
		total += offset
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot open partial download")
	}
	buf := make([]byte, 1<<20)
	var done int64 = offset
	var lastReport time.Time
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", errs.Wrap(errs.Permission, werr, "cannot write model data")
			}
			done += int64(n)
			if progress != nil && (time.Since(lastReport) > 250*time.Millisecond || done == total) {
				progress(done, total)
				lastReport = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", errs.Wrap(errs.Network, rerr, "download interrupted at %d bytes", done).
				WithHint("run the same command again to resume")
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", errs.Wrap(errs.Internal, err, "cannot flush model data")
	}
	if err := f.Close(); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot close model data")
	}
	if wantSHA != "" {
		sum, err := fileSHA256(part)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(sum, wantSHA) {
			os.Remove(part)
			return "", errs.New(errs.Integrity, "SHA-256 mismatch for %s", rawURL).
				WithHint("expected %s, got %s; the download was discarded", wantSHA, sum)
		}
	}
	if _, err := gguf.ParseFile(part); err != nil {
		os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dst); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot finalize model file")
	}
	return dst, nil
}

// Remove deletes one model directory.
func (s *Store) Remove(name string) error {
	e, err := s.Get(name)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return errs.Wrap(errs.Internal, err, "cannot resolve models root")
	}
	dir, err := filepath.Abs(e.Dir)
	if err != nil {
		return errs.Wrap(errs.Internal, err, "cannot resolve model directory")
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		return errs.New(errs.Conflict, "refusing to remove %s outside %s", dir, root)
	}
	// Remove the config first so a crash leaves no half-valid model.
	if err := os.Remove(s.ConfigPath(e.Name)); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.Permission, err, "cannot remove model configuration")
	}
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot remove %s", dir)
	}
	return nil
}

func (s *Store) tokenAllowed(u *url.URL) bool {
	for _, h := range s.TokenHosts {
		if h != "" && strings.EqualFold(u.Hostname(), h) {
			return true
		}
	}
	return false
}

func isRemote(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
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

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errs.Wrap(errs.NotFound, err, "cannot open %s", path)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot hash %s", path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func linkOrCopy(src, dst string, st os.FileInfo, progress func(int64, int64)) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		// Cross-device or a filesystem without hard links: copy.
	}
	in, err := os.Open(src)
	if err != nil {
		return errs.Wrap(errs.NotFound, err, "cannot open %s", src)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create %s", dst)
	}
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(dst)
				return errs.Wrap(errs.Permission, werr, "cannot write %s", dst)
			}
			done += int64(n)
			if progress != nil {
				progress(done, st.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(dst)
			return errs.Wrap(errs.Internal, rerr, "cannot copy model")
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return errs.Wrap(errs.Internal, err, "cannot close %s", dst)
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}
