package main

import (
	"bufio"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	stdlibhttp "github.com/andrewhowdencom/stdlib/http"
)

func TestProviderHTTPClientDisabled(t *testing.T) {
	client, closeFile, err := providerHTTPClient("")
	if err != nil || client != nil {
		t.Fatalf("disabled client = %v, err = %v", client, err)
	}
	if err := closeFile(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderHTTPClientKeyLogAndStreamingTimeouts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.log")
	client, closeFile, err := providerHTTPClient(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFile() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key log permissions = %o, want 600", info.Mode().Perm())
	}
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %s, want no total timeout", client.Timeout)
	}
	instrumented, ok := client.Transport.(*stdlibhttp.InstrumentedTransport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	transport, ok := instrumented.Base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport type = %T", instrumented.Base)
	}
	if transport.ResponseHeaderTimeout != 10*time.Minute {
		t.Fatalf("response header timeout = %s", transport.ResponseHeaderTimeout)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.KeyLogWriter == nil {
		t.Fatal("TLS key log writer missing")
	}
}

func TestProviderHTTPClientRejectsSharedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := providerHTTPClient(path); err == nil {
		t.Fatal("expected shared key log file to be rejected")
	}
}

func TestProviderHTTPClientLogsStreamedTLSExchange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "keys.log")
	client, closeFile, err := providerHTTPClient(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFile() }()
	transport := client.Transport.(*stdlibhttp.InstrumentedTransport).Base.(*http.Transport)
	certs := x509.NewCertPool()
	certs.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = certs

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			events = append(events, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "data: first,data: second" {
		t.Fatalf("streamed events = %v", events)
	}
	secrets, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secrets), "CLIENT_HANDSHAKE_TRAFFIC_SECRET") && !strings.Contains(string(secrets), "CLIENT_RANDOM") {
		t.Fatalf("TLS key log has no handshake secret: %q", secrets)
	}
}
