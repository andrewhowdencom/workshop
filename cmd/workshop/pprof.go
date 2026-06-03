package main

import (
	"context"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"time"
)

const defaultPProfAddr = "localhost:8715"

// maybeStartPProf starts a background HTTP server with net/http/pprof
// handlers when enabled. The server listens on the given address and
// shuts down gracefully when the supplied context is cancelled. If the
// address is already in use a warning is logged and the function returns
// without blocking the caller.
func maybeStartPProf(ctx context.Context, enabled bool, addr string) {
	if !enabled {
		return
	}

	if addr == "" {
		addr = defaultPProfAddr
	}

	srv := &http.Server{
		Addr: addr,
	}

	go func() {
		slog.Info("starting pprof server", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server failed to start", "addr", addr, "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("pprof server shutdown failed", "error", err)
		}
	}()
}
