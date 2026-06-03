package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"time"
)

const defaultPProfAddr = "localhost:0"

// maybeStartPProf starts a background HTTP server with net/http/pprof
// handlers when enabled. The server listens on the given address and
// shuts down gracefully when the supplied context is cancelled. If the
// address is already in use a warning is logged and the function returns
// without blocking the caller. Returns the actual bound address, or an
// empty string if disabled or if binding failed.
func maybeStartPProf(ctx context.Context, enabled bool, addr string) string {
	if !enabled {
		return ""
	}

	if addr == "" {
		addr = defaultPProfAddr
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("pprof server failed to listen", "addr", addr, "error", err)
		return ""
	}

	actualAddr := ln.Addr().String()
	srv := &http.Server{}

	go func() {
		slog.Info("starting pprof server", "addr", actualAddr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server failed to start", "addr", actualAddr, "error", err)
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

	return actualAddr
}
