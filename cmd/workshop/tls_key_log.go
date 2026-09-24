package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	stdlibhttp "github.com/andrewhowdencom/stdlib/http"
)

// providerHTTPClient creates a client only when TLS key logging is requested.
// The caller must keep the returned file open until provider work has stopped.
func providerHTTPClient(path string) (*http.Client, func() error, error) {
	if path == "" {
		return nil, func() error { return nil }, nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open TLS key log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("stat TLS key log: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, nil, fmt.Errorf("TLS key log must be a regular file accessible only to its owner: %s", path)
	}

	client, err := stdlibhttp.NewClient(
		stdlibhttp.WithTimeout(0), // A streamed response may run indefinitely.
		stdlibhttp.WithConnectTimeout(30*time.Second),
		stdlibhttp.WithTLSHandshakeTimeout(30*time.Second),
		stdlibhttp.WithResponseHeaderTimeout(10*time.Minute),
		stdlibhttp.WithTLSKeyLogWriter(file),
	)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("create provider HTTP client: %w", err)
	}
	return client, file.Close, nil
}
