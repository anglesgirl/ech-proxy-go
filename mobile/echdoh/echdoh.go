// Package echdoh 将本地 DoH 注入服务器导出为 gomobile 库，
// 供 Android GeckoView 浏览器内嵌使用。
//
// 作用：监听 127.0.0.1:8443 HTTPS DoH，对所有域名无条件注入
// Cloudflare 公共 ECH 公钥（ech=），Firefox/GeckoView 配 TRR
// 指向本服务后原生 ECH 自动启用，SNI 隐藏，被墙站点可访问。
//
// 配套 DNS：doh.anglesgirl.eu.org → 127.0.0.1（CF 托管，全球生效，
// 任何设备解析都是本机回环，无需 root 改 hosts）。
// 证书：Let's Encrypt DNS-01 签发的 doh.anglesgirl.eu.org 合法证书，
// 浏览器验证域名证书通过，实际连接落在本机。
package echdoh

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	mu       sync.Mutex
	srv      *http.Server
	running  bool
	upstream []string
	lastErr  string
)

// Start 启动本地 DoH 注入服务器（127.0.0.1:listen）。
// certPEM/keyPEM 为合法域名证书（PEM 文本），upstreams 为逗号分隔的上游 DoH。
func Start(listen string, certPEM, keyPEM, upstreams string) error {
	mu.Lock()
	defer mu.Unlock()
	if running {
		return nil
	}
	if strings.TrimSpace(listen) == "" {
		listen = "127.0.0.1:8443"
	}
	upstream = nil
	for _, u := range strings.Split(upstreams, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			upstream = append(upstream, u)
		}
	}
	if len(upstream) == 0 {
		upstream = []string{
			"https://pieqllv9i7.cloudflare-gateway.com/dns-query",
			"https://162.159.36.5/dns-query",
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", handleDoH)

	// 用 PEM 内容直接构造 TLS 证书（ListenAndServeTLS 只接受文件路径，
	// gomobile 场景拿不到文件系统路径，必须 X509KeyPair 加载内容）。
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		mu.Lock()
		lastErr = "load cert: " + err.Error()
		mu.Unlock()
		return fmt.Errorf("load cert: %w", err)
	}

	s := &http.Server{
		Addr:    listen,
		Handler: mux,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		},
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	srv = s
	running = true
	lastErr = ""

	go func() {
		defer func() {
			if r := recover(); r != nil {
				mu.Lock()
				lastErr = fmt.Sprintf("serve panic: %v", r)
				running = false
				mu.Unlock()
			}
		}()
		// 已用 X509KeyPair 加载证书，Serve 而非 ListenAndServeTLS
		if err := s.ServeTLS(nil, "", ""); err != nil && err != http.ErrServerClosed {
			mu.Lock()
			lastErr = "serve: " + err.Error()
			running = false
			mu.Unlock()
		}
	}()
	return nil
}

// Stop 关闭服务器。安全可重复调用。
func Stop() error {
	mu.Lock()
	s := srv
	srv = nil
	running = false
	mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}

// IsRunning 报告服务是否在运行。
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return running
}

// LastError 返回最近一次启动/运行错误。
func LastError() string {
	mu.Lock()
	defer mu.Unlock()
	return lastErr
}

func handleDoH(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	if r.Method == http.MethodGet {
		b64 := r.URL.Query().Get("dns")
		if b64 == "" {
			http.Error(w, "missing dns param", http.StatusBadRequest)
			return
		}
		var err error
		raw, err = base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			http.Error(w, "bad base64", http.StatusBadRequest)
			return
		}
	} else if r.Method == http.MethodPost {
		buf := make([]byte, 65535)
		n, err := r.Body.Read(buf)
		if err != nil && n == 0 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		raw = buf[:n]
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req := new(dns.Msg)
	if err := req.Unpack(raw); err != nil {
		http.Error(w, "bad dns message", http.StatusBadRequest)
		return
	}
	if len(req.Question) == 0 {
		http.Error(w, "no question", http.StatusBadRequest)
		return
	}
	q := req.Question[0]

	resp, err := queryUpstream(req)
	if err != nil {
		log.Printf("[doh] upstream error for %s %s: %v", q.Name, dns.TypeToString[q.Qtype], err)
		writeError(w, req, dns.RcodeServerFailure)
		return
	}
	resp.Id = req.Id

	if q.Qtype == dns.TypeHTTPS {
		injectECH(resp, q.Name)
	}

	log.Printf("[doh] %s %s -> %d answers (%s)", q.Name, dns.TypeToString[q.Qtype],
		len(resp.Answer), summarizeECH(resp))

	out, err := resp.Pack()
	if err != nil {
		http.Error(w, "pack failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/dns-message")
	w.Header().Set("Cache-Control", "max-age=60")
	w.Write(out)
}

// queryUpstream 用 net/http 走 RFC 8484 GET（application/dns-message 二进制）。
func queryUpstream(req *dns.Msg) (*dns.Msg, error) {
	raw, err := req.Pack()
	if err != nil {
		return nil, err
	}
	b64 := base64.RawURLEncoding.EncodeToString(raw)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	var lastErr error
	for _, u := range upstream {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		full := u + sep + "dns=" + b64
		resp, err := client.Get(full)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
			continue
		}
		out := new(dns.Msg)
		if err := out.Unpack(body); err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	return nil, lastErr
}

// svcbValues 从 HTTPS/SVCB 记录提取 SvcParam 键值列表。
// miekg/dns 对 type 65 的具体类型可能是 *dns.HTTPS 或 *dns.SVCB（同一结构）。
func svcbValues(rr dns.RR) []dns.SVCBKeyValue {
	if svcb, ok := rr.(*dns.SVCB); ok {
		return svcb.Value
	}
	if https, ok := rr.(*dns.HTTPS); ok {
		return https.Value
	}
	return nil
}

// injectECH 无条件注入 CF 公共 ECH 公钥。
//
// 策略（用户拍板，2026-08-14）：不做 AS13335 过滤，所有域名都注入。
// 理由：
//  1. 强制注入 CF 公共公钥对任何站点无害——Firefox 原生 ECH 失败时
//     自动回退普通 TLS（RFC 8446 标准行为），非 CF 站点不受影响；
//  2. CF 托管的被墙站（x.com 等）正是靠这个隐藏 SNI；
//  3. CNAME 链末尾是 CF 的站点（如 video.twimg.com → .cdn.cloudflare.net）
//     也一并覆盖，无需逐一判断。
func injectECH(resp *dns.Msg, name string) {
	name = dns.Fqdn(name)

	// 已有 ech= 就不用注入（尊重站点自己的配置）
	for _, rr := range resp.Answer {
		for _, kv := range svcbValues(rr) {
			if _, isECH := kv.(*dns.SVCBECHConfig); isECH {
				return
			}
		}
	}

	// 获取 CF 公共 ECH 公钥
	echConfig := fetchCFPublicECH()
	if len(echConfig) == 0 {
		log.Printf("[doh] %s: no CF public ECH key available, skip inject", name)
		return
	}

	svcb := &dns.SVCB{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeHTTPS,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Priority: 1,
		Target:   ".",
		Value: []dns.SVCBKeyValue{
			&dns.SVCBECHConfig{ECH: echConfig},
			&dns.SVCBAlpn{Alpn: []string{"h2", "http/1.1"}},
		},
	}
	resp.Answer = append(resp.Answer, svcb)
	resp.Authoritative = true
	log.Printf("[doh] injected ech= into HTTPS record for %s (%d bytes)", name, len(echConfig))
}

// fetchCFPublicECH 获取 Cloudflare 公共 ECH 公钥（cloudflare-ech.com HTTPS ech=）。
func fetchCFPublicECH() []byte {
	q := new(dns.Msg)
	q.SetQuestion("cloudflare-ech.com.", dns.TypeHTTPS)
	resp, err := queryUpstream(q)
	if err != nil {
		log.Printf("[doh] fetchCFPublicECH upstream error: %v", err)
		return nil
	}
	for _, rr := range resp.Answer {
		for _, kv := range svcbValues(rr) {
			if ech, ok := kv.(*dns.SVCBECHConfig); ok {
				return ech.ECH
			}
		}
	}
	return nil
}

func summarizeECH(resp *dns.Msg) string {
	for _, rr := range resp.Answer {
		for _, kv := range svcbValues(rr) {
			if ech, ok := kv.(*dns.SVCBECHConfig); ok {
				return fmt.Sprintf("ech=%dbytes", len(ech.ECH))
			}
		}
	}
	return "no-ech"
}

func writeError(w http.ResponseWriter, req *dns.Msg, rcode int) {
	resp := new(dns.Msg)
	resp.SetRcode(req, rcode)
	out, _ := resp.Pack()
	w.Header().Set("Content-Type", "application/dns-message")
	w.Write(out)
}

var _ = net.ParseIP
var _ = os.Exit
