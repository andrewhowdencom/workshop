package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func getFreeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestPProf_Disabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := getFreeAddr(t)
	maybeStartPProf(ctx, false, addr)

	// Give any accidental goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)

	client := &http.Client{Timeout: 200 * time.Millisecond}
	_, err := client.Get("http://" + addr + "/debug/pprof/")
	if err == nil {
		t.Fatal("expected connection to fail when pprof is disabled")
	}
}

func TestPProf_StartAndServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := getFreeAddr(t)
	maybeStartPProf(ctx, true, addr)

	// Wait for server to start.
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("failed to reach pprof server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
}

func TestPProf_PortInUse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Keep a listener open so the port is occupied.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to occupy port: %v", err)
	}
	defer l.Close()

	addr := l.Addr().String()

	// maybeStartPProf must return immediately even though the port is in use.
	done := make(chan struct{})
	go func() {
		maybeStartPProf(ctx, true, addr)
		close(done)
	}()

	select {
	case <-done:
		// Expected: non-blocking return.
	case <-time.After(2 * time.Second):
		t.Fatal("maybeStartPProf blocked when port was already in use")
	}

	// Wait for the goroutine inside maybeStartPProf to attempt ListenAndServe.
	time.Sleep(100 * time.Millisecond)

	// Ensure the original listener is still reachable (pprof did not steal the port).
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dummy listener no longer reachable: %v", err)
	}
	conn.Close()
}

func TestPProf_ShutdownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	addr := getFreeAddr(t)
	maybeStartPProf(ctx, true, addr)

	// Wait for server to start.
	time.Sleep(100 * time.Millisecond)

	// Verify server is up.
	resp, err := http.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("failed to reach pprof server before cancel: %v", err)
	}
	resp.Body.Close()

	// Cancel context to trigger shutdown.
	cancel()

	// Wait for shutdown goroutine to run.
	time.Sleep(100 * time.Millisecond)

	// Verify server no longer accepts connections.
	client := &http.Client{Timeout: 200 * time.Millisecond}
	_, err = client.Get("http://" + addr + "/debug/pprof/")
	if err == nil {
		t.Fatal("expected connection to fail after context cancellation")
	}

	// Ensure it's a network error rather than a timeout.
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Fatalf("expected a network error after shutdown, got: %v", err)
	}
}
