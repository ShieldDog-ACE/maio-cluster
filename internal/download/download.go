// Package download fetches model files: resume, SHA-256 and GGUF header checks.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var httpClient = &http.Client{}

// Options describes one download.
type Options struct {
	URL          string    // direct download URL
	DestPath     string    // final destination path
	ExpectSHA256 string    // optional expected SHA-256 (a "sha256:" prefix is accepted)
	Log          io.Writer // progress output; may be nil
}

// Fetch downloads to DestPath: writes .part first, renames when complete, resumes via Range.
func Fetch(ctx context.Context, opt Options) error {
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	if st, err := os.Stat(opt.DestPath); err == nil && st.Size() > 0 {
		fmt.Fprintf(opt.Log, "  already present, skipping download: %s\n", opt.DestPath)
		return Verify(opt.DestPath, opt.ExpectSHA256)
	}
	if err := os.MkdirAll(filepath.Dir(opt.DestPath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	part := opt.DestPath + ".part"
	offset := int64(0)
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
		if offset > 0 {
			fmt.Fprintf(opt.Log, "  found partial file, resuming (%s already downloaded)\n", humanBytes(offset))
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to download source: %w", err)
	}
	defer resp.Body.Close()

	var f *os.File
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		f, err = os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	case http.StatusPartialContent:
		f, err = os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	case http.StatusRequestedRangeNotSatisfiable:
		if err := Verify(part, opt.ExpectSHA256); err != nil {
			return err
		}
		return os.Rename(part, opt.DestPath)
	default:
		return fmt.Errorf("download failed: HTTP %d %s", resp.StatusCode, opt.URL)
	}
	if err != nil {
		return fmt.Errorf("failed to open temporary file: %w", err)
	}

	total := int64(-1)
	if resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}
	written := offset
	buf := make([]byte, 256*1024)
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return fmt.Errorf("write failed: %w", werr)
			}
			written += int64(n)
			if time.Since(last) >= time.Second {
				fmt.Fprintf(opt.Log, "\r  downloaded %s", humanBytes(written))
				if total > 0 {
					fmt.Fprintf(opt.Log, " / %s (%d%%)", humanBytes(total), written*100/total)
				}
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			return fmt.Errorf("download interrupted: %w (rerun to resume)", readErr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(opt.Log, "\r  downloaded %s\n", humanBytes(written))
	if err := Verify(part, opt.ExpectSHA256); err != nil {
		return err
	}
	if err := os.Rename(part, opt.DestPath); err != nil {
		return fmt.Errorf("failed to save file: %w", err)
	}
	return nil
}

// CopyLocal puts a local model file into the registry: hardlink first (zero copy), otherwise copy.
func CopyLocal(src, dst string, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.Link(src, dst); err == nil {
		fmt.Fprintf(log, "  hardlinked (zero copy): %s\n", dst)
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer in.Close()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(part)
		return fmt.Errorf("copy failed: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return err
	}
	fmt.Fprintf(log, "  copied (cross-device): %s\n", dst)
	return os.Rename(part, dst)
}

// VerifyMagic checks only the GGUF header; used for local registration to avoid hashing tens of GB.
func VerifyMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("failed to read file header: %w", err)
	}
	if string(magic) != "GGUF" {
		return fmt.Errorf("not a GGUF file (header %q)", string(magic))
	}
	return nil
}

// Verify checks the optional SHA-256 and the GGUF header.
func Verify(path, expectSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if expectSHA != "" {
		want := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expectSHA), "sha256:"))
		if sum != want {
			return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", want, sum)
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("failed to read file header: %w", err)
	}
	if string(magic) != "GGUF" {
		return fmt.Errorf("not a GGUF file (header %q)", string(magic))
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
