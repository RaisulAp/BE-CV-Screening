package server

import (
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/go-chi/chi/v5/middleware"

	"cvscreening/be/internal/auth"
)

// withIdentity reads the auth cookie and, if valid, attaches the identity to the
// context. It NEVER rejects — guests (no cookie) are allowed through; handlers
// decide what to do without an identity (BACKEND.md — guest free tier).
func withIdentity(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
				if idn, err := auth.ParseToken(secret, c.Value); err == nil {
					r = r.WithContext(auth.WithIdentity(r.Context(), idn))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// cors allows only the configured frontend origin and, crucially, permits
// credentials so the auth cookie flows on cross-origin requests (BACKEND.md §9).
//
// /demo/* is an exception: those routes never read cookies/identity (see
// internal/demo), so it's safe to reflect back whatever Origin the caller
// sends (including "null" from a file:// page) with no credentials flag —
// this lets the standalone tester in FE/app/demo be opened directly from
// disk, or served from any port, without needing FRONTEND_URL to match.
func cors(frontendURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			switch {
			case strings.HasPrefix(r.URL.Path, "/demo/"):
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
					w.Header().Set("Access-Control-Max-Age", "300")
				}
			case origin == frontendURL:
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "300")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets the baseline hardening headers (Go has no helmet).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Browsers only honor HSTS over HTTPS, so this is harmless in local HTTP
		// dev — but must be present once deployed behind TLS.
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs one structured line per request. It never logs bodies, so
// auth credentials stay out of the logs (BACKEND.md §9).
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
