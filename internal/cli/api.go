package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/auth"
	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
)

// apiError is a non-2xx response from the maio service.
type apiError struct {
	Status  int
	Code    string
	Message string
	Hint    string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("service returned HTTP %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("service returned HTTP %d: %s", e.Status, e.Message)
}

type apiClient struct {
	base  string
	key   string
	http  *http.Client
	label string
}

func newAPIClient(conf *config.Global) *apiClient {
	host := conf.Host
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	scheme := "http"
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if conf.TLSEnabled() {
		scheme = "https"
		pool := x509.NewCertPool()
		caPath := conf.TLSCA
		if caPath == "" {
			caPath = conf.TLSCert
		}
		if data, err := os.ReadFile(caPath); err == nil {
			pool.AppendCertsFromPEM(data)
			tlsConfig.RootCAs = pool
		} else {
			tlsConfig.InsecureSkipVerify = true
		}
	}
	key, _, _ := auth.Lookup(auth.Options{
		Mode: conf.Auth, Key: conf.APIKey, KeyFile: conf.AuthKeyFile(),
		Host: conf.Host, AllowInsecure: conf.AllowInsecure == 1,
	})
	base := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(conf.Port))
	return &apiClient{
		base: base,
		key:  key,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
				TLSClientConfig:       tlsConfig,
				MaxIdleConns:          8,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 0,
			},
		},
		label: base,
	}
}

func (c *apiClient) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err, "cannot build the service request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "maio/"+Version)
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.Wrap(errs.Timeout, ctx.Err(), "request to the maio service timed out")
		}
		return nil, errs.Wrap(errs.Service, err, "cannot reach the maio service at %s", c.base).
			WithHint("start it with: maio service start  (or: sudo maio boot 1)")
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		ae := &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
		var envelope struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			ae.Code = envelope.Code
			if envelope.Message != "" {
				ae.Message = envelope.Message
			} else if envelope.Error != "" {
				ae.Message = envelope.Error
			}
			ae.Hint = envelope.Hint
		}
		if ae.Status == http.StatusUnauthorized {
			ae.Hint = "read the key with: maio token --show"
		}
		return nil, ae
	}
	return resp, nil
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return errs.Wrap(errs.Internal, err, "cannot encode the request")
		}
		body = bytes.NewReader(data)
	}
	resp, err := c.do(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return errs.Wrap(errs.Service, err, "invalid response from the maio service")
	}
	return nil
}

func (c *apiClient) get(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

func (c *apiClient) post(ctx context.Context, path string, in, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, in, out)
}

func (c *apiClient) reachable(ctx context.Context) bool {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil, "")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

func printAPIError(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		e := errs.New(errs.Service, ae.Message)
		if ae.Hint != "" {
			e.Hint = ae.Hint
		}
		return fail(e)
	}
	return fail(err)
}

func contextShort() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func contextLong() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
