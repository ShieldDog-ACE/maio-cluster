package server

import (
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ShieldDog-ACE/maio-cluster/internal/errs"
	"github.com/ShieldDog-ACE/maio-cluster/internal/logging"
)

// wrap builds the middleware chain. Execution order is recoverer -> access ->
// CORS -> auth -> rate limit -> route.
func (s *Server) wrap(next http.Handler) http.Handler {
	h := s.rateLimit(next)
	h = s.authenticate(h)
	h = s.cors(h)
	h = s.recoverer(h)
	h = s.access(h)
	return h
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.Metrics.IncPanic()
				logging.Error("http.panic", errs.Newf(errs.Internal, "panic while serving %s: %v", r.URL.Path, v),
					"path", r.URL.Path, "stack", truncate(string(debug.Stack()), 4096))
				defer func() { _ = recover() }()
				writeErr(w, r, http.StatusInternalServerError, errs.New(errs.Internal, "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) access(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := s.nextRequestID()
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		rec := &recorder{ResponseWriter: w}
		s.Metrics.AddActive(1)
		defer func() {
			s.Metrics.AddActive(-1)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			endpoint := routeName(r.URL.Path)
			s.Metrics.ObserveRequest(endpoint, rec.status, time.Since(start))
			if !s.accessLog || isProbe(r.URL.Path) {
				return
			}
			levelWarn := rec.status >= 400
			if levelWarn {
				logging.Warn("http.request",
					"method", r.Method, "path", r.URL.Path, "endpoint", endpoint,
					"status", rec.status, "bytes", rec.bytes, "duration_ms", time.Since(start).Milliseconds(),
					"ip", s.clientIP(r), "request_id", id)
			} else {
				logging.Info("http.request",
					"method", r.Method, "path", r.URL.Path, "endpoint", endpoint,
					"status", rec.status, "bytes", rec.bytes, "duration_ms", time.Since(start).Milliseconds(),
					"ip", s.clientIP(r), "request_id", id)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cross-origin access is opt-in: a loopback service without a key must
		// not be callable by arbitrary web pages.
		if s.Conf.CORS == 1 {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProbe(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.Auth == nil || s.Auth.Check(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.Metrics.IncAuthFailure()
		w.Header().Set("WWW-Authenticate", `Bearer realm="maio"`)
		writeErr(w, r, http.StatusUnauthorized, errs.New(errs.Unauthorized, "missing or invalid API key").
			WithHint("send Authorization: Bearer <key>; show the key with: maio token --show"))
	})
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Limiter == nil || isProbe(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ok, retry := s.Limiter.Allow(s.clientIP(r), time.Now())
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		s.Metrics.IncRateLimited()
		secs := int(retry.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeErr(w, r, http.StatusTooManyRequests, errs.New(errs.RateLimited, "rate limit exceeded").
			WithHint("retry after %d seconds, or raise rate_limit in maio.conf", secs))
	})
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.Conf.TrustProxy == 1 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(xr) != nil {
			return xr
		}
	}
	return host
}

func (s *Server) nextRequestID() string {
	seq := s.reqSeq.Add(1)
	return fmt.Sprintf("%x-%x", s.start.UnixNano()&0xffffffff, seq)
}

func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

func routeName(path string) string {
	switch path {
	case "/api/version":
		return "version"
	case "/api/tags":
		return "tags"
	case "/api/ps":
		return "ps"
	case "/api/show":
		return "show"
	case "/api/generate":
		return "generate"
	case "/api/chat":
		return "chat"
	case "/api/embed", "/api/embeddings":
		return "embed"
	case "/api/load":
		return "load"
	case "/api/unload":
		return "unload"
	case "/api/stats":
		return "stats"
	case "/v1/models":
		return "v1_models"
	case "/v1/chat/completions":
		return "v1_chat"
	case "/v1/completions":
		return "v1_completions"
	case "/v1/embeddings":
		return "v1_embeddings"
	case "/metrics":
		return "metrics"
	case "/healthz":
		return "healthz"
	case "/readyz":
		return "readyz"
	default:
		return "other"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// recorder captures the status and byte count without breaking streaming.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		r.ResponseWriter.WriteHeader(code)
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
