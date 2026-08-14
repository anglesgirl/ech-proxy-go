// ech-doh — 本地 DoH 注入服务器
//
// 解决浏览器场景的 ECH 问题：Firefox/GeckoView 原生 ECH 只认目标域 HTTPS
// 记录里的 ech= 参数。x.com 这类 CF 托管站不发布自己的 ech=，原生 ECH
// 不生效 → SNI 明文 → 被墙。
//
// 本服务器监听 127.0.0.1，向 Firefox 的 TRR 提供 DoH（RFC 8484）：
//  1. 上游查真实 A/AAAA/HTTPS 记录（cloudflare-gateway DoH）
//  2. 目标解析到 AS13335 (Cloudflare) 且 HTTPS 记录无 ech= 时，
//     注入 CF 公共 ECH 公钥（cloudflare-ech.com / 内置快照）
//  3. Firefox 收到带 ech= 的 HTTPS 记录 → 原生 ECH 启用 → SNI 隐藏
//
// 配合 Firefox prefs：
//
//	network.trr.mode = 3
//	network.trr.uri = https://<域名>:<port>/dns-query
//	network.trr.bootstrapAddress = 127.0.0.1   (域名直连本机)
//
// 域名需有浏览器信任的合法证书（Let's Encrypt DNS-01 签发，私钥在本机）。
package main

import (
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"
)

var (
	listenAddr  string
	dohUpstream string
	certFile    string
	keyFile     string
	upstreams   []string
)

func main() {
	flag.StringVar(&listenAddr, "listen", "127.0.0.1:8443", "HTTPS listen address")
	flag.StringVar(&dohUpstream, "upstream", "https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query", "upstream DoH endpoints (comma separated)")
	flag.StringVar(&certFile, "cert", "", "TLS cert PEM file")
	flag.StringVar(&keyFile, "key", "", "TLS key PEM file")
	flag.Parse()

	if certFile == "" || keyFile == "" {
		log.Fatal("需要 -cert 和 -key（合法域名证书，浏览器信任）")
	}
	for _, u := range strings.Split(dohUpstream, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			upstreams = append(upstreams, u)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", handleDoH)

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("ech-doh 监听 %s (HTTPS)", listenAddr)
	log.Printf("上游 DoH: %v", upstreams)
	if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil {
		log.Fatal(err)
	}
}

// handleDoH 处理 RFC 8484 DoH 请求（GET ?dns= 或 POST application/dns-message）。
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

	// 逐条查询处理：只处理第一条（DoH 单查询）
	if len(req.Question) == 0 {
		http.Error(w, "no question", http.StatusBadRequest)
		return
	}
	q := req.Question[0]

	// 上游查询
	resp, err := queryUpstream(req)
	if err != nil {
		log.Printf("[doh] upstream error for %s %s: %v", q.Name, dns.TypeToString[q.Qtype], err)
		writeError(w, req, dns.RcodeServerFailure)
		return
	}
	resp.Id = req.Id

	// 注入 ECH（仅 HTTPS 查询 + CF 托管 + 无 ech=）
	if q.Qtype == dns.TypeHTTPS {
		injectECH(resp, q.Name)
	}

	// 记录
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

// queryUpstream 把请求转发给上游 DoH 端点（依次尝试）。
// 用 net/http 走 RFC 8484 GET（application/dns-message 二进制），
// miekg/dns 内置的 Net:"https" 在此版本不可用（unknown network）。
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
	for _, u := range upstreams {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		full := u + sep + "dns=" + b64
		resp, err := client.Get(full)
		if err != nil {
			lastErr = err
			log.Printf("[doh] upstream %s failed: %v", u, err)
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

	// 注入/构造 SVCB 记录
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
	log.Printf("[doh] fetchCFPublicECH: no ech= in %d answers", len(resp.Answer))
	for _, rr := range resp.Answer {
		log.Printf("[doh]   answer: %s (type=%d)", rr.String(), rr.Header().Rrtype)
	}
	return nil
}

func summarizeECH(resp *dns.Msg) string {
	for _, rr := range resp.Answer {
		if svcb, ok := rr.(*dns.SVCB); ok {
			for _, kv := range svcb.Value {
				if ech, ok := kv.(*dns.SVCBECHConfig); ok {
					return fmt.Sprintf("ech=%dbytes", len(ech.ECH))
				}
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
