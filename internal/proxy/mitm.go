package proxy

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// mitmCA 是 CONNECT MITM 模式用的根证书：
// 代理用它对每个目标 host 动态签发证书，客户端需信任该 CA（或跳过证书校验）。
type mitmCA struct {
	mu      sync.Mutex
	key     *ecdsa.PrivateKey
	cert    *x509.Certificate
	certDER []byte
	// host -> 动态签发证书（缓存）
	hosts map[string]*tls.Certificate
}

func newMitmCA() (*mitmCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ech-proxy MITM CA", Organization: []string{"ech-proxy-go"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &mitmCA{
		cert:     cert,
		certDER:  der,
		key:      key,
		hosts:    make(map[string]*tls.Certificate),
	}, nil
}

// certForHost 返回对该 host 签发的证书（带缓存）
func (c *mitmCA) certForHost(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tc, ok := c.hosts[host]; ok {
		return tc, nil
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: host, Organization: []string{"ech-proxy-go"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(72 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, template, c.cert, &leafKey.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	tc := &tls.Certificate{
		Certificate: [][]byte{leafDER, c.certDER},
		PrivateKey:  leafKey,
	}
	c.hosts[host] = tc
	return tc, nil
}

// caPEM 导出 CA 证书（PEM，供用户安装信任）
func (c *mitmCA) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.certDER})
}

// tlsServerConfig 构建 MITM 下游 TLS 配置（客户端要信任 CA 或跳过校验）
func (c *mitmCA) tlsServerConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return c.certForHost(hello.ServerName)
		},
		MinVersion: tls.VersionTLS12,
	}
}

// handleConnectMitm 处理 CONNECT MITM：客户端 TLS（自签证书）终止后，
// 逐个 HTTP 请求转发给上游（appLayerClient 自动 DoH+ECH），响应回写。
// 客户端只要信任代理 CA 或跳过校验，即可获得完整 ECH 直连，无需改协议。
func (s *Server) handleConnectMitm(clientConn net.Conn, host, port string) {
	host = strings.ToLower(host)
	log.Printf("[mitm] CONNECT %s:%s -> MITM session", host, port)

	// 客户端侧 TLS（自签名证书）
	tlsConn := tls.Server(clientConn, s.mitm.tlsServerConfig())
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("[mitm] client TLS handshake failed for %s: %v", host, err)
		return
	}
	log.Printf("[mitm] client TLS established for %s (peer certs=%d)", host, len(tlsConn.ConnectionState().PeerCertificates))

	// 手动 HTTP 读请求循环（keep-alive 复用同一 TLS 连接）
	br := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			if err != io.EOF {
				log.Printf("[mitm] read request end for %s: %v", host, err)
			}
			return
		}
		// 处理请求并写响应；false 表示连接结束
		if !s.serveMitmRequestDirect(tlsConn, req, host) {
			return
		}
	}
}

// serveMitmRequestDirect 处理单个请求并写响应到 conn（返回 false 表示连接结束）
func (s *Server) serveMitmRequestDirect(conn net.Conn, r *http.Request, host string) bool {
	outURL := &url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, outURL.String(), r.Body)
	if err != nil {
		log.Printf("[mitm] bad request: %v", err)
		writeMitmError(conn, http.StatusBadGateway, "mitm: bad request: "+err.Error())
		return false
	}
	for k, vv := range r.Header {
		if hopByHopHeader(k) || k == "Host" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Host = host
	req.Header.Del("Accept-Encoding")

	resp, err := s.appLayerClient.Do(req)
	if err != nil {
		log.Printf("[mitm] upstream error %s %s: %v", r.Method, r.URL.Path, err)
		writeMitmError(conn, http.StatusBadGateway, "mitm: upstream error: "+err.Error())
		return false
	}
	defer func() {
		resp.Body.Close()
		r.Body.Close()
	}()
	log.Printf("[mitm] %s %s -> %s (%s)", r.Method, r.URL.Path, resp.Status, host)

	// 组装响应头
	var hb bytes.Buffer
	code := resp.StatusCode
	fmt.Fprintf(&hb, "HTTP/1.1 %d %s\r\n", code, resp.Status)
	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", rewriteLocation(loc, host))
	}
	for k, vv := range resp.Header {
		if hopByHopHeader(k) {
			continue
		}
		for _, v := range vv {
			fmt.Fprintf(&hb, "%s: %s\r\n", k, v)
		}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[mitm] read upstream body failed: %v", err)
		return false
	}
	fmt.Fprintf(&hb, "Content-Length: %d\r\n", len(body))
	fmt.Fprintf(&hb, "Connection: keep-alive\r\n\r\n")
	if _, err := conn.Write(hb.Bytes()); err != nil {
		return false
	}
	if _, err := conn.Write(body); err != nil {
		return false
	}
	return true
}

// writeMitmError 写简单 HTTP 错误响应（MITM 隧道内）
func writeMitmError(w io.Writer, code int, msg string) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
}