// Package echproxy exposes the reusable proxy as a small gomobile-friendly API.
//
// Android applications should call this package for lifecycle only. DNS, ECH,
// TLS retry/fallback, caching, and CONNECT handling remain in the internal
// packages owned by this repository.
package echproxy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/proxy"
)

var (
	mu       sync.Mutex
	server   *proxy.Server
	lastInfo = "not started"
)

// Start launches a loopback-only HTTP CONNECT proxy.
//
// listen must be an address such as "127.0.0.1:34043". doh accepts one or more
// comma-separated RFC 8484 DoH endpoints. cachePath may be empty, but Android
// callers should provide an app-private file path to retain public ECH configs.
// noDowngrade controls whether a host with ECH metadata may fall back to plain
// TLS after ECH failure.
func Start(listen, doh, cachePath string, noDowngrade bool) error {
	mu.Lock()
	defer mu.Unlock()

	if server != nil {
		return nil
	}
	if listen == "" {
		return fmt.Errorf("listen address is required")
	}
	if doh == "" {
		return fmt.Errorf("DoH endpoint is required")
	}

	cfg := config.Default()
	cfg.Listen = listen
	cfg.DoH = doh
	cfg.Mode = "http"
	cfg.DNS.CachePath = cachePath
	cfg.ECH.NoDowngrade = noDowngrade
	if err := cfg.Validate(); err != nil {
		return err
	}

	s := proxy.New(cfg)
	server = s
	lastInfo = "starting on " + listen
	go func() {
		err := s.ListenAndServe()
		mu.Lock()
		defer mu.Unlock()
		if server == s {
			server = nil
		}
		if err != nil {
			lastInfo = "stopped: " + err.Error()
		} else {
			lastInfo = "stopped"
		}
	}()
	lastInfo = "listening on " + listen
	return nil
}

// Stop gracefully shuts down the current proxy. It is safe to call repeatedly.
func Stop() error {
	mu.Lock()
	s := server
	server = nil
	mu.Unlock()
	if s == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.Shutdown(ctx)
	mu.Lock()
	lastInfo = "stopped"
	mu.Unlock()
	return err
}

// IsRunning reports whether this process currently owns a started proxy.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return server != nil
}

// LastStatus returns lifecycle status. Detailed ECH acceptance is emitted by
// the core tlsconn logger; applications should collect their bounded Go log
// bridge when exposing diagnostics.
func LastStatus() string {
	mu.Lock()
	defer mu.Unlock()
	return lastInfo
}
