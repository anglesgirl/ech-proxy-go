// Package tlsconn 实现 ECH TLS 连接
package tlsconn

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/dns"
)

// Dialer ECH TLS 拨号器
type Dialer struct {
	timeout      time.Duration
	skipVerify   bool
	fallbackPlain bool
}

// New 创建 ECH 拨号器
func New(timeout time.Duration, skipVerify, fallbackPlain bool) *Dialer {
	return &Dialer{
		timeout:       timeout,
		skipVerify:    skipVerify,
		fallbackPlain: fallbackPlain,
	}
}

// DialECH 建立 ECH TLS 连接
// hostname: 目标域名（用于 SNI）
// result: DNS 查询结果（含 ECHConfig）
func (d *Dialer) DialECH(hostname string, result *dns.Result) (net.Conn, error) {
	if len(result.IPs) == 0 {
		return nil, fmt.Errorf("no IP for %s", hostname)
	}

	ip := result.IPs[0]
	addr := net.JoinHostPort(ip.String(), "443")

	tlsConfig := &tls.Config{
		ServerName:         hostname,
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: d.skipVerify,
	}

	// 配置 ECH
	if result.ECH != nil {
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

	conn, err := d.dialTLS(addr, tlsConfig)
	if err != nil && result.ECH != nil && d.fallbackPlain {
		// ECH 失败，回退到普通 TLS
		log.Printf("[tls] ECH failed for %s: %v, falling back to plain TLS", hostname, err)
		plainConfig := &tls.Config{
			ServerName:         hostname,
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: d.skipVerify,
		}
		conn, err = d.dialTLS(addr, plainConfig)
		if err != nil {
			return nil, fmt.Errorf("TLS handshake %s: %w", addr, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("TLS handshake %s: %w", addr, err)
	}

	return conn, nil
}

func (d *Dialer) dialTLS(addr string, cfg *tls.Config) (net.Conn, error) {
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

	// 检查 ECH 是否被接受
	state := tlsConn.ConnectionState()
	if cfg.EncryptedClientHelloConfigList != nil && !state.ECHAccepted {
		log.Printf("[tls] WARNING: ECH was not accepted by server for %s", addr)
	}

	return tlsConn, nil
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
