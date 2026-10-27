package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.Warn("http.encode_failed", "error", err.Error())
	}
}

// writeErr writes the stable maio error envelope and records the failure. 4xx
// responses are warnings; 5xx responses are errors.
func writeErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	e := errs.As(err)
	body := map[string]any{"error": err.Error()}
	if e != nil {
		body["code"] = string(e.Code)
		body["message"] = e.Msg
		if h := errs.HintOf(err); h != "" {
			body["hint"] = h
		}
	}
	path, ip, rid := "", "", ""
	if r != nil {
		if r.URL != nil {
			path = r.URL.Path
		}
		ip = r.RemoteAddr
		rid = r.Header.Get("X-Request-ID")
	}
	if status >= 500 {
		logging.Error("http.error", err, "status", status, "path", path, "ip", ip, "request_id", rid)
	} else {
		logging.Warn("http.error", "status", status, "path", path, "ip", ip, "request_id", rid,
			"error", err.Error())
	}
	writeJSON(w, status, body)
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeErr(w, r, http.StatusMethodNotAllowed, errs.Newf(errs.Usage, "method not allowed; use %s", allow))
}

func statusFor(err error) int {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return http.StatusRequestEntityTooLarge
	}
	e := errs.As(err)
	if e == nil {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case errs.NotFound:
		return http.StatusNotFound
	case errs.Usage, errs.Config:
		return http.StatusBadRequest
	case errs.Permission:
		return http.StatusForbidden
	case errs.Unauthorized:
		return http.StatusUnauthorized
	case errs.RateLimited, errs.Busy:
		return http.StatusTooManyRequests
	case errs.Timeout:
		return http.StatusGatewayTimeout
	case errs.Hardware:
		return http.StatusServiceUnavailable
	case errs.Engine, errs.Verify, errs.Download:
		return http.StatusBadGateway
	case errs.Interrupted:
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

// decodeJSON decodes one JSON body with the configured size cap. Unknown
// fields are accepted because Ollama and OpenAI clients send optional keys.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return errs.New(errs.Usage, "request body is empty")
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.Conf.MaxBodyMB)<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errs.Wrap(errs.Usage, err, "request body exceeds the %d MiB limit", s.Conf.MaxBodyMB).
				WithHint("send a smaller request, or raise max_body_mb in maio.conf")
		}
		return errs.Newf(errs.Usage, "invalid JSON body: %v", err).
			WithHint("send a JSON object; the body limit is %d MiB", s.Conf.MaxBodyMB)
	}
	return nil
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errs.New(errs.Usage, "request body is empty")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(s.Conf.MaxBodyMB)<<20))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errs.Wrap(errs.Usage, err, "request body exceeds the %d MiB limit", s.Conf.MaxBodyMB).
				WithHint("send a smaller request, or raise max_body_mb in maio.conf")
		}
		return nil, err
	}
	if len(body) == 0 {
		return nil, errs.New(errs.Usage, "request body is empty")
	}
	return body, nil
}

func modelFromBody(body []byte) string {
	var probe struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if probe.Model != "" {
		return probe.Model
	}
	return probe.Name
}
