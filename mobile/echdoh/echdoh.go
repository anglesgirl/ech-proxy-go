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

	"github.com/anglesgirl/ech-proxy-go/internal/cloudflare"
	"github.com/miekg/dns"
)

var (
	mu       sync.Mutex
	srv      *http.Server
	running  bool
	upstream []string
	lastErr  string
	logBuf   []string // Go 侧日志缓冲（Kotlin 轮询拉取）
	logBufMu sync.Mutex
	logPos   int
)

// PollLogs 增量返回 Go 侧日志（Kotlin 定时轮询写入 echbrowser.log）。
// gomobile 不支持导出 func 参数回调，用轮询替代。
func PollLogs() string {
	logBufMu.Lock()
	defer logBufMu.Unlock()
	if logPos >= len(logBuf) {
		return ""
	}
	out := ""
	for i := logPos; i < len(logBuf); i++ {
		out += logBuf[i] + "\n"
	}
	logPos = len(logBuf)
	return out
}

// slog 带缓冲的日志：既写 stderr（logcat），也进缓冲供 Kotlin 拉取。
func slog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[doh] %s", msg)
	logBufMu.Lock()
	logBuf = append(logBuf, msg)
	logBufMu.Unlock()
}

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
		// 已用 X509KeyPair 加载证书。必须用 ListenAndServeTLS（自动创建
		// listener）；ServeTLS(nil,...) 传 nil listener 会 panic。
		if err := s.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
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
		slog("upstream error for %s %s: %v", q.Name, dns.TypeToString[q.Qtype], err)
		writeError(w, req, dns.RcodeServerFailure)
		return
	}
	resp.Id = req.Id

	if q.Qtype == dns.TypeHTTPS {
		injectECH(resp, q.Name)
	}
	// A 记录改写：若目标（跟随 CNAME）是 CF 托管但 IP 大陆被墙，
	// 替换为 DoH 端点 IP（162.159.36.x 实测可达）。
	if q.Qtype == dns.TypeA {
		rewriteAIfCF(resp, q.Name)
	}
	// AAAA 清空：CF 托管站点强制 IPv4（DoH 端点 IP），避免 Firefox
	// 优先 IPv6 超时。非 CF 站点原样保留。
	if q.Qtype == dns.TypeAAAA {
		if isCloudflareHosted(q.Name) {
			rewriteAAAAEmpty(resp, q.Name)
		}
	}

	slog("%s %s -> %d answers (%s)", q.Name, dns.TypeToString[q.Qtype],
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
// 策略（2026-08-14 修正）：只给 CF 托管域名（跟随 CNAME 链判断）注入。
// 理由：
//  1. CF 托管被墙站（x.com 等）靠注入隐藏 SNI —— 必须注入；
//  2. Fastly 托管（abs/cdn.syndication.twimg.com 等 CSS/JS 资源）**不能注入**：
//     Fastly 不认 CF 公共公钥 → ECH 握手失败 → Firefox 降级明文并缓存 24h
//     → 该域名整个废掉（用户实测页面 CSS 全丢）；
//  3. 非 CF 站点（baidu 等）同样不注入，避免多余 ECH 尝试。
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

	// 只给 CF 托管域名注入（跟随 CNAME 链判断，同 rewriteAIfCF）
	if !isCloudflareHosted(name) {
		slog("%s: not CF-hosted, skip inject", name)
		return
	}

	// 获取 CF 公共 ECH 公钥
	echConfig := fetchCFPublicECH()
	if len(echConfig) == 0 {
		slog("%s: no CF public ECH key available, skip inject", name)
		return
	}

	// 注入 ipv4hint=DoH 端点 IP：目标域自己的 A 记录（如 x.com 172.66.0.227）
	// 在大陆可能被墙，而 DoH 端点 IP（162.159.36.x）实测可达。RFC 9460
	// 规定客户端优先用 SVCB 的 ipv4hint 连接，从而绕开被墙边缘。
	hintIPs := fetchDohEndpointIPv4s()

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
			// ⚠️ 只留 http/1.1：xprobe CLI 实测 HTTP/1.1+ECH→200，
			// Firefox 用 h2+ECH 页面失败（CF 边缘 ECH+h2 组合异常）。
			&dns.SVCBAlpn{Alpn: []string{"http/1.1"}},
		},
	}
	if len(hintIPs) > 0 {
		hints := make([]net.IP, 0, len(hintIPs))
		for _, h := range hintIPs {
			if ip := net.ParseIP(h); ip != nil {
				hints = append(hints, ip)
			}
		}
		if len(hints) > 0 {
			svcb.Value = append(svcb.Value, &dns.SVCBIPv4Hint{Hint: hints})
		}
	}
	resp.Answer = append(resp.Answer, svcb)
	resp.Authoritative = true
	slog("injected ech= into HTTPS record for %s (%d bytes, hints=%v)", name, len(echConfig), hintIPs)
}

// isCloudflareHosted 跟随 CNAME 链（≤5 跳）查询目标 A 记录，判断是否
// 全部为 CF 边缘（AS13335）。用于 injectECH 决定是否注入（Fastly 等
// 非 CF 域名不注入，避免 ECH 失败降级明文缓存 24h）。
func isCloudflareHosted(name string) bool {
	cur := dns.Fqdn(name)
	for hop := 0; hop < 5; hop++ {
		q := new(dns.Msg)
		q.SetQuestion(cur, dns.TypeA)
		r, err := queryUpstream(q)
		if err != nil {
			return false
		}
		var ips []string
		cur = ""
		for _, rr := range r.Answer {
			switch v := rr.(type) {
			case *dns.A:
				ips = append(ips, v.A.String())
			case *dns.CNAME:
				cur = dns.Fqdn(v.Target)
			}
		}
		if len(ips) > 0 {
			return cloudflare.AllAS13335(ips)
		}
		if cur == "" {
			return false
		}
	}
	return false
}

// rewriteAIfCF 若目标域名（跟随 CNAME 链）最终解析到 CF 边缘（AS13335），
// 则把 A 记录改写为 DoH 端点 IP（162.159.36.x，大陆实测可达）。
//
// 覆盖 x.com 全家桶：api.x.com / video.twimg.com(.cdn.cloudflare.net) 等
// 响应只有 CNAME 的域名也跟随判断，避免漏改导致 Firefox 直连被墙边缘。
func rewriteAIfCF(resp *dns.Msg, name string) {
	var ips []string
	var cname string
	for _, rr := range resp.Answer {
		switch r := rr.(type) {
		case *dns.A:
			ips = append(ips, r.A.String())
		case *dns.CNAME:
			cname = r.Target
		}
	}

	// 无 A 但有 CNAME：跟随 CNAME 再查（最多 5 跳）
	hops := 0
	cur := cname
	for len(ips) == 0 && cur != "" && hops < 5 {
		q := new(dns.Msg)
		q.SetQuestion(dns.Fqdn(cur), dns.TypeA)
		r2, err := queryUpstream(q)
		if err != nil {
			break
		}
		cur = ""
		for _, rr := range r2.Answer {
			switch r := rr.(type) {
			case *dns.A:
				ips = append(ips, r.A.String())
			case *dns.CNAME:
				cur = r.Target
			}
		}
		hops++
	}
	if len(ips) == 0 {
		slog("%s: no A records (cname=%s), keep as-is", name, cname)
		return
	}
	if !cloudflare.AllAS13335(ips) {
		slog("%s: A=%v not CF (cname=%s), keep original", name, ips, cname)
		return
	}

	hintIPs := fetchDohEndpointIPv4s()
	if len(hintIPs) == 0 {
		return
	}
	newAnswers := make([]dns.RR, 0, len(resp.Answer)+len(hintIPs))
	for _, rr := range resp.Answer {
		switch rr.(type) {
		case *dns.A, *dns.CNAME:
			continue // 丢弃原 A/CNAME
		default:
			newAnswers = append(newAnswers, rr)
		}
	}
	seen := map[string]bool{}
	for _, ip := range hintIPs {
		if seen[ip] {
			continue
		}
		seen[ip] = true
		newAnswers = append(newAnswers, &dns.A{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: net.ParseIP(ip),
		})
	}
	resp.Answer = newAnswers
	slog("%s: CF-hosted (cname=%s) A=%v -> rewritten to %v", name, cname, ips, hintIPs)
}

// rewriteAAAAEmpty 对 CF 托管域名返回空 AAAA（NODATA），强制 Firefox 走
// IPv4（改写的 DoH 端点 IP），避免等待 IPv6 超时。
func rewriteAAAAEmpty(resp *dns.Msg, name string) {
	if len(resp.Answer) == 0 {
		return
	}
	slog("%s: AAAA cleared (force IPv4)", name)
	resp.Answer = nil
}

// fetchDohEndpointIPv4s 解析 DoH 端点域名（如 pieqllv9i7.cloudflare-gateway.com）
// 的 IPv4，作为注入记录的 ipv4hint。这些 IP 是 CF 边缘，实测大陆可达。
// 解析失败时用内置快照兜底（同一批网关的已知 IP）。
func fetchDohEndpointIPv4s() []string {
	var ips []string
	seen := map[string]bool{}
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip != "" && !seen[ip] {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}

	// 从 upstream DoH 域名解析 A 记录
	for _, u := range upstream {
		host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		if net.ParseIP(host) != nil {
			continue // 已经是 IP 直连（如 162.159.36.5）
		}
		q := new(dns.Msg)
		q.SetQuestion(dns.Fqdn(host), dns.TypeA)
		resp, err := queryUpstream(q)
		if err != nil {
			continue
		}
		for _, rr := range resp.Answer {
			if a, ok := rr.(*dns.A); ok {
				add(a.A.String())
			}
		}
	}

	// 内置快照兜底（pieqllv9i7.cloudflare-gateway.com 已知 IP）
	for _, ip := range []string{"162.159.36.5", "162.159.36.20"} {
		add(ip)
	}
	return ips
}

// fetchCFPublicECH 获取 Cloudflare 公共 ECH 公钥（cloudflare-ech.com HTTPS ech=）。
func fetchCFPublicECH() []byte {
	q := new(dns.Msg)
	q.SetQuestion("cloudflare-ech.com.", dns.TypeHTTPS)
	resp, err := queryUpstream(q)
	if err != nil {
		slog("fetchCFPublicECH upstream error: %v", err)
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
