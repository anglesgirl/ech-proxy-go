// Package proxy implements HTTP CONNECT and SOCKS5 proxy.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/dns"
	"github.com/anglesgirl/ech-proxy-go/internal/tlsconn"
)

// Server is the proxy server.
type Server struct {
	cfg      *config.Config
	resolver *dns.Resolver
	dialer   *tlsconn.Dialer
	server   *http.Server
}

// New creates a proxy server from configuration.
func New(cfg *config.Config) *Server {
	tlsTimeout, _ := time.ParseDuration(cfg.TLS.Timeout)
	dnsTimeout, _ := time.ParseDuration(cfg.DNS.Timeout)
	cacheTTL, _ := time.ParseDuration(cfg.DNS.CacheTTL)
	connectTimeout, _ := time.ParseDuration(cfg.Proxy.ConnectTimeout)
	idleTimeout, _ := time.ParseDuration(cfg.Proxy.IdleTimeout)

	if tlsTimeout == 0 {
		tlsTimeout = 15 * time.Second
	}
	if dnsTimeout == 0 {
		dnsTimeout = 10 * time.Second
	}
	if cacheTTL == 0 {
		cacheTTL = 300 * time.Second
	}

	resolver := dns.NewWithCache(cfg.DoH, dnsTimeout, cacheTTL, cfg.DNS.CachePath)

	// No-downgrade mode disables plain TLS fallback for ECH-capable hosts.
	fallbackPlain := cfg.TLS.FallbackPlain
	if cfg.ECH.NoDowngrade {
		fallbackPlain = false
	}

	dialer := tlsconn.New(tlsTimeout, cfg.TLS.SkipVerify, fallbackPlain)
	if cfg.ECH.CustomIPs != "" {
		dialer.SetCustomIPs(cfg.ECH.CustomIPs)
		log.Printf("[proxy] custom edge IPs configured: %s", cfg.ECH.CustomIPs)
	}
	// ECH 握手被拒且服务器给了 retry_configs 时,缓存到磁盘供下次直接使用。
	dialer.SetRetryConfigSink(func(host string, config []byte) {
		resolver.CacheECHConfig(host, config)
		log.Printf("[proxy] cached server retry_configs for %s (%d bytes)", host, len(config))
	})

	srv := &Server{
		cfg:      cfg,
		resolver: resolver,
		dialer:   dialer,
	}

	srv.server = &http.Server{
		Addr:         cfg.Listen,
		Handler:      http.HandlerFunc(srv.handleHTTP),
		ReadTimeout:  connectTimeout,
		WriteTimeout: 0, // CONNECT needs long-lived connections
		IdleTimeout:  idleTimeout,
	}

	return srv
}

// ListenAndServe starts the proxy.
func (s *Server) ListenAndServe() error {
	mode := strings.ToLower(s.cfg.Mode)
	log.Printf("[proxy] starting on %s (mode=%s doh=%s)", s.cfg.Listen, mode, s.cfg.DoH)

	if mode == "socks5" || mode == "both" {
		go s.listenSOCKS5()
	}

	if mode == "http" || mode == "both" {
		return s.server.ListenAndServe()
	}

	select {} // socks5 only: block forever
}

// Shutdown gracefully stops the proxy.
func (s *Server) Shutdown(ctx context.Context) error {
	log.Printf("[proxy] shutting down...")
	return s.server.Shutdown(ctx)
}

// handleHTTP handles HTTP CONNECT requests.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = "443"
	}

	log.Printf("[http] CONNECT %s:%s", host, port)

	if port != "443" {
		s.handlePlainForward(w, r, host, port)
		return
	}

	result, err := s.resolver.Lookup(host, s.cfg.DNS.PreferIPv4)
	if err != nil {
		log.Printf("[http] DoH failed for %s: %v", host, err)
		http.Error(w, "DNS lookup failed", http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	clientConn, _, err := hj.Hijack()
	if err != nil {
		log.Printf("[http] hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	targetConn, err := s.dialer.DialECH(host, result)
	if err != nil {
		log.Printf("[http] ECH dial failed for %s: %v", host, err)
		return
	}
	defer targetConn.Close()

	log.Printf("[http] connected %s (%s) ech=%v", host, result.IPs[0], result.ECH != nil)

	Relay(clientConn, targetConn)
}

// handlePlainForward handles non-HTTPS TCP forwarding.
func (s *Server) handlePlainForward(w http.ResponseWriter, r *http.Request, host, port string) {
	target := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	clientConn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()
	defer conn.Close()

	Relay(clientConn, conn)
}

// listenSOCKS5 starts the SOCKS5 proxy on port+1.
func (s *Server) listenSOCKS5() {
	addr := s.socks5Addr()
	log.Printf("[socks5] starting on %s", addr)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[socks5] listen failed: %v", err)
		return
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[socks5] accept failed: %v", err)
			continue
		}
		go s.handleSOCKS5(conn)
	}
}

func (s *Server) socks5Addr() string {
	host, port, err := net.SplitHostPort(s.cfg.Listen)
	if err != nil {
		return s.cfg.Listen + "1"
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", parseInt(port)+1))
}

func (s *Server) handleSOCKS5(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 256)
	_, err := conn.Read(buf)
	if err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	conn.Write([]byte{0x05, 0x00})

	n, err := conn.Read(buf)
	if err != nil || n < 7 {
		return
	}
	if buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}

	var host string
	var port int
	switch buf[3] {
	case 0x01:
		host = net.IP(buf[4:8]).String()
		port = int(buf[8])<<8 | int(buf[9])
	case 0x03:
		domainLen := int(buf[4])
		host = string(buf[5 : 5+domainLen])
		port = int(buf[5+domainLen])<<8 | int(buf[6+domainLen])
	case 0x04:
		host = net.IP(buf[4:20]).String()
		port = int(buf[20])<<8 | int(buf[21])
	default:
		return
	}

	log.Printf("[socks5] CONNECT %s:%d", host, port)

	var targetConn net.Conn

	if port == 443 && isDomain(host) {
		result, err := s.resolver.Lookup(host, s.cfg.DNS.PreferIPv4)
		if err != nil {
			log.Printf("[socks5] DoH failed: %v", err)
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		targetConn, err = s.dialer.DialECH(host, result)
		if err != nil {
			log.Printf("[socks5] ECH dial failed: %v", err)
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
	} else {
		targetConn, err = net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 10*time.Second)
		if err != nil {
			log.Printf("[socks5] dial failed: %v", err)
			conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
	}
	defer targetConn.Close()

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	Relay(conn, targetConn)
}

// Relay bidirectionally forwards data between two connections.
func Relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(a, b)
		done <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		io.Copy(b, a)
		done <- struct{}{}
	}()

	<-done
	a.Close()
	b.Close()
	wg.Wait()
}

func isDomain(s string) bool {
	return net.ParseIP(s) == nil
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
