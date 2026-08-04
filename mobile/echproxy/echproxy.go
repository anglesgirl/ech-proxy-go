// Package echproxy exposes the reusable proxy as a small gomobile-friendly API.
//
// Android applications should call this package for lifecycle only. DNS, ECH,
// TLS retry/fallback, caching, and CONNECT handling remain in the internal
// packages owned by this repository.
package echproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/proxy"
)

type boundedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *boundedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.b.Write(p)
	if l.b.Len() > 64*1024 {
		data := l.b.Bytes()
		l.b.Reset()
		_, _ = l.b.Write(data[len(data)-48*1024:])
	}
	return len(p), nil
}

func (l *boundedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var (
	mu       sync.Mutex
	server   *proxy.Server
	lastInfo = "not started"
	logs     = &boundedLog{}
)

// Start launches a loopback-only HTTP CONNECT proxy.
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

	logs = &boundedLog{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
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

func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return server != nil
}

// LastStatus returns the concise lifecycle status. Use Diagnostics for the
// bounded protocol log.
func LastStatus() string {
	mu.Lock()
	defer mu.Unlock()
	return lastInfo
}

// Diagnostics returns lifecycle status plus the bounded Go proxy log.
func Diagnostics() string {
	mu.Lock()
	info := lastInfo
	mu.Unlock()
	return info + "\n--- go proxy log ---\n" + logs.String()
}
