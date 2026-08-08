// Package tlsconn implements ECH TLS connection establishment.
//
// Improvements ported from production ECH proxy:
//   - ECH rejection retry_configs: uses server-provided retry config on rejection
//   - No-downgrade mode: protected hosts never fall back to plain TLS
//   - Android cert pool: loads system CA store for CGO-free binaries
//   - Custom IP support: operator-supplied edge IPs tried first
//   - Multi-candidate dialing: tries all resolved IPs before failing
package tlsconn

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/certutil"
	"github.com/anglesgirl/ech-proxy-go/internal/cloudflare"
	"github.com/anglesgirl/ech-proxy-go/internal/dns"
)

// Dialer establishes ECH TLS connections.
type Dialer struct {
	timeout       time.Duration
	skipVerify    bool
	fallbackPlain bool // ECH failed → plain TLS (set false for protected hosts)
	customIPs     []string
	certPool      *tls.Config
	// onRetryConfig, when set, persists a server-provided retry_configs to the
	// disk cache so the next connection handshakes straight from cache.
	onRetryConfig func(host string, config []byte)
}

// SetRetryConfigSink registers a callback that receives server-provided
// retry_configs after a successful ECH rejection retry, so they can be cached.
func (d *Dialer) SetRetryConfigSink(fn func(host string, config []byte)) {
	d.onRetryConfig = fn
}

// New creates an ECH dialer.
func New(timeout time.Duration, skipVerify, fallbackPlain bool) *Dialer {
	d := &Dialer{
		timeout:       timeout,
		skipVerify:    skipVerify,
		fallbackPlain: fallbackPlain,
	}
	if pool := certutil.LoadSystemCertPool(); pool != nil {
		d.certPool = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return d
}

// SetCustomIPs configures operator-supplied edge IPs to try before DNS.
// Only AS13335 (Cloudflare) IPs are accepted.
func (d *Dialer) SetCustomIPs(ipList string) {
	d.customIPs = cloudflare.FilterAS13335(cloudflare.ParseIPList(ipList))
}

// DialECH establishes an ECH TLS connection to hostname using DNS results.
func (d *Dialer) DialECH(hostname string, result *dns.Result) (net.Conn, error) {
	if len(result.IPs) == 0 {
		return nil, fmt.Errorf("no IP for %s", hostname)
	}

	// Build candidate address list: DoH-resolved IPs first, then custom IPs.
	port := "443"
	var candidates []string
	for _, ip := range result.IPs {
		candidates = append(candidates, net.JoinHostPort(ip.String(), port))
	}
	for _, ip := range d.customIPs {
		candidates = append(candidates, net.JoinHostPort(ip, port))
	}

	tlsConfig := &tls.Config{
		ServerName:         hostname,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: d.skipVerify,
		NextProtos:         []string{"h2", "http/1.1"},
	}
	if d.certPool != nil {
		tlsConfig.RootCAs = d.certPool.RootCAs
	}

	// Configure ECH if available.
	hasECH := result.ECH != nil && len(result.ECH.Config) > 0
	if hasECH {
		// ECH 只在 TLS 1.3 中定义，故 ECH 连接必须强制 1.3。
		tlsConfig.MinVersion = tls.VersionTLS13
		tlsConfig.EncryptedClientHelloConfigList = result.ECH.Config
		if result.OuterSNI != "" {
			outerName := strings.TrimSuffix(result.OuterSNI, ".")
			if outerName != "" {
				tlsConfig.ServerName = outerName
			}
		}
		echB64 := base64.StdEncoding.EncodeToString(result.ECH.Config)
		log.Printf("[tls] ECH for %s -> outer=%s ech=%s...(len=%d)",
			hostname, tlsConfig.ServerName, truncStr(echB64, 40), len(result.ECH.Config))
	} else {
		log.Printf("[tls] no ECHConfig for %s, plain TLS", hostname)
	}

	// Try each candidate address.
	var lastErr error
	for _, addr := range candidates {
		conn, err := d.dialTLS(addr, tlsConfig, hostname)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		log.Printf("[tls] handshake via %s failed: %v", addr, err)

		// ECH rejection: try server-provided retry_configs.
		var rej *tls.ECHRejectionError
		if hasECH && errors.As(err, &rej) && len(rej.RetryConfigList) > 0 {
			log.Printf("[tls] ECH rejected via %s; retrying with server retry_configs", addr)
			retryConfig := tlsConfig.Clone()
			retryConfig.EncryptedClientHelloConfigList = rej.RetryConfigList
			conn, retryErr := d.dialTLS(addr, retryConfig, hostname)
			if retryErr == nil {
				if tlsConn, ok := conn.(*tls.Conn); ok && tlsConn.ConnectionState().ECHAccepted {
					log.Printf("[tls] ECH accepted via %s (retry_configs)", addr)
				}
				// 缓存 server retry_configs,下次直接用它握手。
				if d.onRetryConfig != nil {
					d.onRetryConfig(hostname, rej.RetryConfigList)
				}
				return conn, nil
			}
			log.Printf("[tls] retry_configs also failed via %s: %v", addr, retryErr)
		}
	}

	// Fallback to plain TLS (only if allowed and ECH was attempted).
	if hasECH && d.fallbackPlain {
		log.Printf("[tls] all ECH attempts failed for %s, falling back to plain TLS", hostname)
		plainConfig := &tls.Config{
			ServerName: hostname,
			// plain TLS 兼容老 CDN（只支持 TLS 1.2，如内容页静态资源源站）。
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: d.skipVerify,
			NextProtos:         []string{"h2", "http/1.1"},
		}
		if d.certPool != nil {
			plainConfig.RootCAs = d.certPool.RootCAs
		}
		for _, addr := range candidates {
			conn, err := d.dialTLS(addr, plainConfig, hostname)
			if err == nil {
				log.Printf("[tls] plain TLS fallback succeeded via %s", addr)
				return conn, nil
			}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no candidates to dial")
	}
	return nil, fmt.Errorf("TLS handshake failed for %s after %d candidate(s): %w",
		hostname, len(candidates), lastErr)
}

func (d *Dialer) dialTLS(addr string, cfg *tls.Config, hostname string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: d.timeout}
	rawConn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", addr, err)
	}

	tlsConn := tls.Client(rawConn, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	defer cancel()

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, err
	}

	// Log ECH acceptance status.
	state := tlsConn.ConnectionState()
	if cfg.EncryptedClientHelloConfigList != nil {
		if state.ECHAccepted {
			log.Printf("[tls] ECH ACCEPTED for %s via %s (TLS %s, ALPN %q)",
				hostname, addr, tlsVersionName(state.Version), state.NegotiatedProtocol)
		} else {
			log.Printf("[tls] WARNING: ECH was NOT accepted by server for %s via %s", hostname, addr)
		}
	}

	return tlsConn, nil
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// DialerWithCache wraps Dialer with an ECH config cache for repeated lookups.
type DialerWithCache struct {
	*Dialer
	resolver *dns.Resolver
	echMu    sync.Mutex
	echCache map[string][]byte
}

// NewWithCache creates a dialer that caches ECH configs per host.
func NewWithCache(d *Dialer, resolver *dns.Resolver) *DialerWithCache {
	return &DialerWithCache{
		Dialer:   d,
		resolver: resolver,
		echCache: make(map[string][]byte),
	}
}

// DialContext implements the http.Transport DialTLSContext interface.
// It resolves the host via DoH, fetches ECH config, and establishes a TLS connection.
func (dc *DialerWithCache) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	result, err := dc.resolver.Lookup(host, true)
	if err != nil {
		return nil, fmt.Errorf("DoH lookup %s: %w", host, err)
	}

	dc.ensureECH(host, result)

	return dc.DialECH(host, result)
}

// ensureECH fills result.ECH via the full ECH config chain when the plain
// Lookup found none (target's own HTTPS record has no ech=). The chain is:
// disk cache → cloudflare-ech.com (Cloudflare's official ECH public key,
// valid for all AS13335-hosted zones) → target's own HTTPS ech=.
// CF-hosted zones that don't publish ech= themselves (e.g. hanime1.me)
// still get ECH through Cloudflare's official public key instead of
// silently downgrading to plain TLS and leaking SNI.
func (dc *DialerWithCache) ensureECH(host string, result *dns.Result) {
	if result.ECH != nil && len(result.ECH.Config) > 0 {
		return
	}
	b, outer, err := dc.resolver.FetchECHConfig(host)
	if err != nil || len(b) == 0 {
		return
	}
	result.ECH = &dns.ECHConfig{Config: b}
	if outer != "" {
		result.OuterSNI = outer
	}
	log.Printf("[tls] ECH config for %s from fallback chain (outer=%s, len=%d)",
		host, outer, len(b))
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
