// Package download fetches model files with resume support, checksum and GGUF
// verification, disk-space checks, retries and clear error hints.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/hardware"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

const (
	progressEvery = time.Second
	minFreeBytes  = 64 << 20
	userAgent     = "maio/2"
)

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
	},
}

// Options describes one download.
type Options struct {
	URL          string    // direct download URL
	DestPath     string    // final destination path
	ExpectSHA256 string    // optional expected SHA-256 (a "sha256:" prefix is accepted)
	Log          io.Writer // progress output; may be nil
	Client       *http.Client
	MaxRetries   int // additional attempts after the first; default 2
}

// Fetch downloads to DestPath: writes .part first, renames when complete,
// resumes via Range, retries transient failures and keeps the partial file on
// interruption.
func Fetch(ctx context.Context, opt Options) error {
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	if opt.Client == nil {
		opt.Client = httpClient
	}
	if opt.MaxRetries < 0 {
		opt.MaxRetries = 0
	} else if opt.MaxRetries == 0 {
		opt.MaxRetries = 2
	}
	if st, err := os.Stat(opt.DestPath); err == nil && st.Size() > 0 {
		fmt.Fprintf(opt.Log, "  already present, verifying: %s\n", opt.DestPath)
		if err := Verify(opt.DestPath, opt.ExpectSHA256); err != nil {
			return errs.Wrap(errs.Verify, err, "existing file failed verification: %s", opt.DestPath).
				WithHint("delete it and retry the download")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(opt.DestPath), 0o755); err != nil {
		return errs.Wrap(errs.Permission, err, "cannot create download directory %s", filepath.Dir(opt.DestPath)).
			WithHint("check directory permissions, or choose another download root")
	}
	part := opt.DestPath + ".part"
	offset := int64(0)
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
		if offset > 0 {
			fmt.Fprintf(opt.Log, "  found partial file, resuming (%s already downloaded)\n", humanBytes(uint64(offset)))
			logging.Info("download.resume", "url", opt.URL, "dest", opt.DestPath, "offset", offset)
		}
	}

	logging.Info("download.start", "url", opt.URL, "dest", opt.DestPath, "offset", offset)
	// Check free space up front when the server reports a size.
	remote := RemoteSize(ctx, opt.Client, opt.URL)
	if remote > 0 {
		need := uint64(remote)
		if offset > 0 && uint64(offset) < need {
			need -= uint64(offset)
		}
		if err := EnsureSpace(filepath.Dir(opt.DestPath), need); err != nil {
			return err
		}
	}

	attempts := opt.MaxRetries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			backoff := time.Duration(attempt-1) * time.Second
			fmt.Fprintf(opt.Log, "  retrying in %s (attempt %d/%d)\n", backoff, attempt, attempts)
			logging.Warn("download.retry", "url", opt.URL, "attempt", attempt, "backoff_ms", backoff.Milliseconds())
			select {
			case <-ctx.Done():
				return interrupted(part, ctx.Err())
			case <-time.After(backoff):
			}
		}
		done, err := fetchOnce(ctx, opt, part, &offset, remote)
		if done || err == nil {
			return err
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return errs.Wrap(errs.Download, lastErr, "download failed after %d attempts: %v", attempts, lastErr).
		WithHint("check the network and mirror, then rerun the command to resume")
}

// fetchOnce performs one HTTP attempt. done=true means the file is complete or
// the error is terminal.
func fetchOnce(ctx context.Context, opt Options, part string, offset *int64, remote int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
	if err != nil {
		return true, errs.Wrap(errs.Download, err, "invalid download URL %s", opt.URL).
			WithHint("use an http(s) URL or a local GGUF path")
	}
	req.Header.Set("User-Agent", userAgent)
	if *offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *offset))
	}
	resp, err := opt.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return true, interrupted(part, ctx.Err())
		}
		return false, markRetryable(errs.Wrap(errs.Download, err, "cannot connect to %s", opt.URL).
			WithHint("check the network, DNS and the mirror setting in maio.conf"))
	}
	defer resp.Body.Close()

	var f *os.File
	switch resp.StatusCode {
	case http.StatusOK:
		if *offset > 0 {
			fmt.Fprintf(opt.Log, "  server does not support resume; restarting the download\n")
			*offset = 0
		}
		f, err = os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	case http.StatusPartialContent:
		if start, _ := parseContentRangeRange(resp.Header.Get("Content-Range")); start >= 0 && start != *offset {
			return true, errs.Newf(errs.Download, "the server resumed at byte %d instead of %d", start, *offset).
				WithHint("delete %s.part and rerun the download", part)
		}
		f, err = os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	case http.StatusRequestedRangeNotSatisfiable:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			if st, statErr := os.Stat(part); statErr != nil || st.Size() != total {
				_ = os.Remove(part)
				return true, errs.Newf(errs.Download, "the partial file size does not match the server (expected %d bytes)", total).
					WithHint("rerun the command to download from the beginning")
			}
		}
		if err := Verify(part, opt.ExpectSHA256); err == nil {
			return true, renamePart(part, opt.DestPath)
		}
		_ = os.Remove(part)
		return true, errs.Newf(errs.Download, "the server rejected the resume range; the partial file was removed").
			WithHint("rerun the command to download from the beginning")
	case http.StatusUnauthorized, http.StatusForbidden:
		return true, errs.Newf(errs.Download, "download denied by the server (HTTP %d): %s", resp.StatusCode, opt.URL).
			WithHint("the source requires authentication; use a public mirror or --url with a reachable link")
	case http.StatusNotFound:
		return true, errs.Newf(errs.Download, "file not found (HTTP 404): %s", opt.URL).
			WithHint("check the model name or pass --url with the exact file link")
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return false, markRetryable(errs.Newf(errs.Download, "download source returned HTTP %d: %s", resp.StatusCode, opt.URL))
	default:
		return true, errs.Newf(errs.Download, "download failed: HTTP %d %s", resp.StatusCode, opt.URL).
			WithHint("check the URL in a browser or try the mirror")
	}
	if err != nil {
		return true, errs.Wrap(errs.Permission, err, "cannot open temporary file %s", part)
	}

	total := int64(-1)
	if resp.ContentLength > 0 {
		total = *offset + resp.ContentLength
	} else if remote > 0 {
		total = remote
	}
	written := *offset
	buf := make([]byte, 256*1024)
	lastLog := time.Now()
	lastSpaceCheck := int64(0)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				*offset = written
				return true, errs.Wrap(errs.IO, werr, "write failed after %s", humanBytes(uint64(written))).
					WithHint("check free disk space and write permissions")
			}
			written += int64(n)
			if time.Since(lastLog) >= progressEvery {
				progress(opt.Log, written, total)
				lastLog = time.Now()
			}
			if written-lastSpaceCheck >= 64<<20 {
				lastSpaceCheck = written
				if free, _, _, err := hardware.DiskUsage(filepath.Dir(opt.DestPath)); err == nil && free < minFreeBytes {
					_ = f.Close()
					*offset = written
					return true, errs.Newf(errs.IO, "disk is full while downloading (%s free)", hardware.HumanBytes(free)).
						WithHint("free disk space, then rerun the command to resume")
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = f.Close()
			*offset = written
			if ctx.Err() != nil {
				return true, interrupted(part, ctx.Err())
			}
			return false, markRetryable(errs.Wrap(errs.Download, readErr, "download interrupted at %s", humanBytes(uint64(written))).
				WithHint("rerun the command to resume from the partial file"))
		}
	}
	progress(opt.Log, written, total)
	fmt.Fprintln(opt.Log)
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return true, errs.Wrap(errs.IO, err, "cannot flush the downloaded file")
	}
	if err := f.Close(); err != nil {
		return true, errs.Wrap(errs.IO, err, "cannot close the downloaded file")
	}
	if total > 0 && written != total {
		*offset = written
		return false, markRetryable(errs.Newf(errs.Download, "download is incomplete: got %s of %s", humanBytes(uint64(written)), humanBytes(uint64(total))).
			WithHint("rerun the command to resume"))
	}
	if err := Verify(part, opt.ExpectSHA256); err != nil {
		return true, err
	}
	if err := renamePart(part, opt.DestPath); err != nil {
		return true, err
	}
	logging.Info("download.done", "url", opt.URL, "dest", opt.DestPath, "bytes", written)
	return true, nil
}

func renamePart(part, dest string) error {
	if err := os.Rename(part, dest); err != nil {
		return errs.Wrap(errs.IO, err, "cannot save the downloaded file to %s", dest).
			WithHint("check write permissions and free disk space")
	}
	return nil
}

func progress(w io.Writer, written, total int64) {
	if total > 0 {
		pct := written * 100 / total
		fmt.Fprintf(w, "\r  downloaded %s / %s (%d%%)", humanBytes(uint64(written)), humanBytes(uint64(total)), pct)
		return
	}
	fmt.Fprintf(w, "\r  downloaded %s", humanBytes(uint64(written)))
}

func interrupted(part string, cause error) error {
	return errs.Wrap(errs.Interrupted, cause, "download interrupted").
		WithHint("the partial file was kept (%s); rerun the command to resume", part)
}

// RemoteSize asks the server for the file size. It returns 0 when unknown.
func RemoteSize(ctx context.Context, client *http.Client, rawURL string) int64 {
	if client == nil {
		client = httpClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", userAgent)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
				return resp.ContentLength
			}
		}
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPartialContent {
		return parseContentRange(resp.Header.Get("Content-Range"))
	}
	if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return 0
}

func parseContentRange(v string) int64 {
	total, _ := parseContentRangeTotal(v)
	return total
}

func parseContentRangeTotal(v string) (int64, bool) {
	// bytes 0-0/12345
	if i := strings.LastIndexByte(v, '/'); i >= 0 {
		if n, err := strconv.ParseInt(strings.TrimSpace(v[i+1:]), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func parseContentRangeRange(v string) (start, total int64) {
	// bytes 100-199/1000
	start, total = -1, -1
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(strings.ToLower(v), "bytes ") {
		return -1, -1
	}
	rest := strings.TrimSpace(v[len("bytes "):])
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	dash := strings.IndexByte(rest, '-')
	if dash < 0 {
		return -1, -1
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(rest[:dash]), 10, 64); err == nil {
		start = n
	}
	return start, total
}

// EnsureSpace rejects an operation when the destination filesystem has less
// than need + 10% (and at least 64 MiB) free.
func EnsureSpace(dir string, need uint64) error {
	total, free, _, err := hardware.DiskUsage(dir)
	if err != nil || total == 0 {
		return nil // do not block on an unknown filesystem
	}
	required := need + need/10
	if required < need+minFreeBytes {
		required = need + minFreeBytes
	}
	if free < required {
		return errs.Newf(errs.IO, "not enough disk space in %s: need %s, free %s",
			dir, hardware.HumanBytes(required), hardware.HumanBytes(free)).
			WithHint("free disk space, or choose another download root")
	}
	return nil
}

// CopyLocal puts a local model file into the registry: hardlink first (zero
// copy), otherwise copy. The caller verifies the result.
func CopyLocal(src, dst string, log io.Writer) (string, error) {
	if log == nil {
		log = io.Discard
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", errs.Wrap(errs.NotFound, err, "cannot read local model %s", src).
			WithHint("check the path and file permissions")
	}
	if !st.Mode().IsRegular() {
		return "", errs.Newf(errs.Usage, "local source is not a regular file: %s", src)
	}
	if _, err := os.Stat(dst); err == nil {
		return "", errs.Newf(errs.Conflict, "destination already exists: %s", dst)
	}
	if err := EnsureSpace(filepath.Dir(dst), uint64(st.Size())); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot create model directory %s", filepath.Dir(dst))
	}
	if err := os.Link(src, dst); err == nil {
		fmt.Fprintf(log, "  hardlinked (zero copy): %s\n", dst)
		logging.Info("register.hardlink", "source", src, "dest", dst, "bytes", st.Size())
		return "hardlink", nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot open local model %s", src)
	}
	defer in.Close()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return "", errs.Wrap(errs.Permission, err, "cannot create %s", part)
	}
	written, copyErr := io.Copy(out, in)
	if copyErr != nil {
		_ = out.Close()
		_ = os.Remove(part)
		return "", errs.Wrap(errs.IO, copyErr, "copy failed after %s", humanBytes(uint64(written))).
			WithHint("the partial copy was removed; check free disk space and retry")
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(part)
		return "", errs.Wrap(errs.IO, err, "cannot flush the copied file")
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(part)
		return "", errs.Wrap(errs.IO, err, "cannot close the copied file")
	}
	fmt.Fprintf(log, "  copied (cross-device): %s\n", dst)
	logging.Info("register.copy", "source", src, "dest", dst, "bytes", written)
	if err := os.Rename(part, dst); err != nil {
		return "", errs.Wrap(errs.IO, err, "cannot save the copied model to %s", dst)
	}
	return "copy", nil
}

// VerifyMagic checks only the GGUF header; used for local registration to avoid
// hashing tens of GB.
func VerifyMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errs.Wrap(errs.NotFound, err, "model file not found: %s", path)
		}
		return errs.Wrap(errs.Permission, err, "cannot open model file %s", path)
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return errs.Wrap(errs.Verify, err, "cannot read the GGUF header of %s", path).
			WithHint("the file may be truncated or not a GGUF model")
	}
	if string(magic) != "GGUF" {
		return errs.Newf(errs.Verify, "not a GGUF file: %s (header %q)", path, string(magic)).
			WithHint("expected a file that starts with GGUF; check the source")
	}
	return nil
}

// Verify checks the optional SHA-256 and the GGUF header.
func Verify(path, expectSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errs.Wrap(errs.NotFound, err, "model file not found: %s", path)
		}
		return errs.Wrap(errs.Permission, err, "cannot read model file %s", path)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return errs.Wrap(errs.IO, err, "cannot hash %s", path)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if expectSHA != "" {
		want := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expectSHA), "sha256:"))
		if sum != want {
			return errs.Newf(errs.Verify, "SHA-256 mismatch for %s: expected %s, got %s", path, want, sum).
				WithHint("delete the file and retry; the mirror may serve a different or corrupt file")
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errs.Wrap(errs.IO, err, "cannot rewind %s", path)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(f, header); err != nil {
		return errs.Wrap(errs.Verify, err, "cannot read the GGUF header of %s", path).
			WithHint("the file may be truncated; re-download it")
	}
	if string(header) != "GGUF" {
		return errs.Newf(errs.Verify, "not a GGUF file: %s (header %q)", path, string(header)).
			WithHint("expected a file that starts with GGUF; check the download source")
	}
	return nil
}

// FileNameFromURL returns a safe basename for a URL, or "".
func FileNameFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" || base == ".." {
		return ""
	}
	return base
}

func humanBytes(n uint64) string { return hardware.HumanBytes(n) }

// retryableError marks transient failures that are safe to retry after the
// partial file offset has been advanced.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func markRetryable(err error) error { return &retryableError{err: err} }

func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}
