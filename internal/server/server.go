package server

import (
	"context"
	"net/http"
	"time"
)

// New wraps an http.Server with sensible timeouts.
func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// ReadHeaderTimeout guards against slowloris; the body (PDF upload) can
		// be larger/slower, so ReadTimeout is generous.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Shutdown gracefully drains in-flight HTTP requests.
func Shutdown(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}
