// Package proxy implements HTTP CONNECT and SOCKS5 proxy.
package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/cloudflare"
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
	// appLayerClient 处理应用层转发(X-Ech-Target 模式):OkHttp 发来明文
	// HTTP 请求 + X-Ech-Target 头,代理用 ECH 连上游、返回明文响应。
	// 与 CONNECT 隧道不同:客户端不需要自己再 TLS 握手。
	appLayerClient *http.Client
	// mitm 非空时启用 CONNECT MITM 模式（下游自签 TLS + 上游 ECH）
	mitm *mitmCA
}

// builtinDoHHostIPs 是 Cloudflare Gateway DoH 端点域名当前解析的 IP 快照
// (2026-08-13 实测 pieqllv9i7.cloudflare-gateway.com → 162.159.36.5/20)。
// 这些 IP 属于 AS13335,且部分区域(福建)封禁目标站点 CF 边缘 IP 时
// DoH 端点 IP 仍可达(否则 DoH 连不上)。作为 resolveDoHHostIPs 的兜底,
// 系统 DNS 也被污染时仍能拿到可用候选。
var builtinDoHHostIPs = []string{"162.159.36.5", "162.159.36.20"}

// resolveDoHHostIPs 解析 DoH 端点域名(如 pieqllv9i7.cloudflare-gateway.com)
// 的 IP 列表,用于自动并入 ECH 握手候选。系统 DNS 解析失败时回退内置快照。
// ⚠️ IPv4 强制优先（2026-08-14 实测）: 同一 ECH 请求连 CF IPv4 边缘 → 200,
// 连 CF IPv6 边缘 → 403(bot 判定不同, IPv6 节点信誉低)。IPv6 仅作最后兜底。
func resolveDoHHostIPs(dohURL string) []string {
	u, err := url.Parse(dohURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	host := u.Hostname()

	// 1. 系统 DNS 解析(DoH 端点域名一般未被污染)。
	// ⚠️ net.LookupHost 无超时：移动宽带被污染的系统 DNS 能卡 30s+
	// （2026-08-15 CO3 实测：冷启动 Start() 卡 30s）。3s 超时走快照。
	var v4, v6 []string
	type lookupRes struct {
		addrs []string
		err   error
	}
	lch := make(chan lookupRes, 1)
	go func() {
		addrs, err := net.LookupHost(host)
		lch <- lookupRes{addrs, err}
	}()
	select {
	case r := <-lch:
		if r.err == nil {
			for _, a := range r.addrs {
				ip := net.ParseIP(a)
				if ip == nil {
					continue
				}
				if ip.To4() != nil {
					v4 = append(v4, a)
				} else {
					v6 = append(v6, a)
				}
			}
		}
	case <-time.After(3 * time.Second):
		// 超时：跳过系统 DNS，直接用内置快照
	}
	// 2. 内置快照兜底。
	for _, b := range builtinDoHHostIPs {
		found := false
		for _, a := range v4 {
			if a == b {
				found = true
				break
			}
		}
		if !found {
			v4 = append(v4, b)
		}
	}
	return append(v4, v6...)
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
	// 自定义边缘 IP 配置(用户显式填写)。
	if cfg.ECH.CustomIPs != "" {
		dialer.SetCustomIPs(cfg.ECH.CustomIPs)
		log.Printf("[proxy] custom edge IPs configured: %s", cfg.ECH.CustomIPs)
	}
	// ⚠️ 2026-08-15 移除 DoH 端点 IP 并入：162.159.36.x(Gateway 段)不是
	// 目标域官方段。实测(CO3 05:24 日志): 并发 dial 时 Gateway 段延迟低
	// 胜出 → 目标站返回 CF 1034(Edge IP Restricted)；同一官方 IP 串行
	// 使用(如 Han1meViewer)稳定 200。结论：只连目标域官方段, 不连
	// Gateway 段。CO3 副本已同步移除。
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

	// MITM 模式：初始化动态 CA（客户端需信任该 CA 或跳过校验）
	if cfg.MITM.Enabled {
		ca, err := newMitmCA()
		if err != nil {
			log.Printf("[proxy] MITM CA init failed: %v", err)
		} else {
			srv.mitm = ca
			log.Printf("[proxy] MITM mode enabled (client must trust proxy CA or skip verify)")
		}
	}

	// 应用层转发:用 DialerWithCache 作为 DialTLSContext,
	// http.Client 的每个请求都会经 DoH + ECH(或普通 TLS) 连上游。
	appLayerDialer := tlsconn.NewWithCache(dialer, resolver)
	// ⚠️ 2026-08-13 实测: 必须有 cookie jar! CF jsd 验证完成时 Set-Cookie
	// cf_clearance(代理转发响应,client.Do 自动存 jar,按 archiveofourown.org
	// 域)→ 登录 POST 转发时 jar 按 URL 域匹配自动带上 cf_clearance →
	// AO3 放行。没有 jar → POST 无验证凭据 → 302 auth_error(playwright 实测)。
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("[proxy] cookiejar init failed: %v", err)
	}
	srv.appLayerClient = &http.Client{
		Transport: &http.Transport{
			DialTLSContext:    appLayerDialer.DialContext,
			ForceAttemptHTTP2: true, // Go>=1.21 默认 false；必须显式开启 HTTP/2
			Proxy:             nil,
		},
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
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

// SetEndpoints hot-updates the DoH endpoints and custom edge IPs without
// restarting the proxy. Called after the seed TXT config arrives.
func (s *Server) SetEndpoints(doh, ip string) {
	doh = strings.TrimSpace(doh)
	ip = strings.TrimSpace(ip)
	if doh != "" {
		urls := make([]string, 0)
		for _, u := range strings.Split(doh, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			s.resolver.SetDoHURLs(urls)
		}
	}
	if ip != "" {
		s.dialer.SetCustomIPs(ip)
		log.Printf("[proxy] custom edge IPs updated: %s", ip)
	}
}

// SetPreferredIPs 将测速优选的 IP 前置到候选最前（不丢现有 custom IPs：
// 远程配置 IP / DoH 端点 IP 保留在优选 IP 之后）。移动宽带下优选 IP
// 是实测最快的边缘，优先尝试可避免串行试不可达 IP 白等。
func (s *Server) SetPreferredIPs(ips []string) {
	if len(ips) == 0 {
		return
	}
	s.dialer.PrependCustomIPs(ips)
	log.Printf("[proxy] preferred IPs prepended: %v", ips)
}

// SetOverrides hot-updates per-host fixed IP lists (seed TXT `override=`)
// field). Hosts listed bypass DoH A/AAAA and dial with plain TLS only.
func (s *Server) SetOverrides(spec string) {
	s.resolver.SetOverrides(spec)
}

// GetMitmCAPem 返回 MITM CA 证书（PEM 格式），供外部导出/安装信任
func (s *Server) GetMitmCAPem() []byte {
	if s.mitm != nil {
		return s.mitm.caPEM()
	}
	return nil
}

// handleHTTP handles HTTP requests: application-layer forwarding
// (X-Ech-Target mode) and HTTP CONNECT tunnels.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Application-layer forwarding: OkHttp (via EchInterceptor) rewrites
	// https://host/path -> http://127.0.0.1:port/path + "X-Ech-Target: host".
	// The proxy completes ECH/plain-TLS itself and returns cleartext HTTP,
	// so the client never re-handshakes TLS on top (avoiding double TLS).
	if r.Method != http.MethodConnect {
		s.handleAppLayer(w, r)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = "443"
	}

	log.Printf("[http] CONNECT %s:%s", host, port)

	// CONNECT 隧道语义:;
	// 模式A（默认）: 纯 TCP 转发，客户端自己 TLS 握手（SNI 明文），代理只做 DoH 去污染。
	// 模式B（MITM，需 cfg.MITM 启用）: 代理终止客户端 TLS（自签证书）再以 ECH/普通 TLS
	//   连上游——客户端只需信任代理 CA（或跳过校验），任何不改协议的 App 都能获得 ECH。
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

	// MITM 模式：接管 TLS，客户端无需改协议（libmpv/系统播放器/WebView 全兼容）
	if s.mitm != nil {
		s.handleConnectMitm(clientConn, host, port)
		return
	}

	// DoH 解析目标（带缓存/override），失败回退系统 DNS
	targetAddr := net.JoinHostPort(host, port)
	if result, rerr := s.resolver.Lookup(host, s.cfg.DNS.PreferIPv4); rerr == nil && len(result.IPs) > 0 {
		targetAddr = net.JoinHostPort(result.IPs[0].String(), port)
		log.Printf("[http] CONNECT %s via DoH %s", host, result.IPs[0])
	} else if rerr != nil {
		log.Printf("[http] CONNECT %s DoH failed (%v), fallback system DNS", host, rerr)
	}

	targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		log.Printf("[http] tcp dial %s: %v", targetAddr, err)
		return
	}
	defer targetConn.Close()

	Relay(clientConn, targetConn)
}

// handleAppLayer proxies a cleartext HTTP request whose target host is carried
// in the X-Ech-Target header. It forwards the request to the upstream over an
// ECH (or plain TLS) connection and writes the upstream response back verbatim.
func (s *Server) handleAppLayer(w http.ResponseWriter, r *http.Request) {
	target, reqPath, ok := resolveTarget(strings.TrimSpace(r.Header.Get("X-Ech-Target")), r.URL.Path)
	if !ok {
		// No target and not CONNECT: not something we proxy.
		http.Error(w, "missing X-Ech-Target", http.StatusBadRequest)
		return
	}
	if !validHost(target) {
		http.Error(w, "echproxy: invalid target host", http.StatusBadRequest)
		return
	}
	target = strings.ToLower(target)

	outURL := &url.URL{Scheme: "https", Host: target, Path: reqPath, RawQuery: r.URL.RawQuery}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, outURL.String(), r.Body)
	if err != nil {
		http.Error(w, "echproxy: bad request: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		if hopByHopHeader(k) || k == "Host" || k == "X-Ech-Target" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Host = target
	// 应用层域名改写: WebView/App 的请求 URL 是 127.0.0.1(本地代理地址),
	// Referer/Origin 头泄漏非官方域名 → AO3 服务端拒绝登录(auth_error,
	// 2026-08-13 playwright 实测: 表单提交 Referer=http://127.0.0.1:PORT)。
	// ECH 只保护网络层(TLS 隐藏 SNI),应用层来源必须与真实一致——重写为
	// 官方域名,服务端看到的来源与直连官方完全相同。
	if v := req.Header.Get("Referer"); v != "" {
		if u, err := url.Parse(v); err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") {
			u.Scheme = "https"
			u.Host = target
			req.Header.Set("Referer", u.String())
		}
	}
	if v := req.Header.Get("Origin"); v != "" {
		if u, err := url.Parse(v); err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") {
			u.Scheme = "https"
			u.Host = target
			req.Header.Set("Origin", u.String())
		}
	}
	req.Header.Del("Accept-Encoding")

	resp, err := s.appLayerClient.Do(req)
	if err != nil {
		log.Printf("[http] app-layer upstream error %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "echproxy: upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	log.Printf("[http] app-layer %s %s -> %s", target, r.URL.Path, resp.Status)

	if loc := resp.Header.Get("Location"); loc != "" {
		resp.Header.Set("Location", rewriteLocation(loc, target))
	}

	// HLS 播放列表必须重写:把相对/绝对路径引用改成基于原始域名(target)
	// 的绝对 URL。否则播放器会按 127.0.0.1:<port>/... 解析分片路径,
	// 丢失上游 host(X-Ech-Target),请求直接打到本机代理却无 target → 404。
	// 仅当响应体确实是 m3u8 时才重写;ts/mp4 等二进制分片原样透传。
	//
	// ⚠️ 2026-08-06 修复:重写必须在 WriteHeader **之前**完成,并且必须
	// 重新设置 Content-Length。原来在 WriteHeader 之后 Set(Content-Length)
	// 是无效的——CDN 原始长度被透传,重写后 body 变长/变短,ExoPlayer
	// 按错误长度截断读取 m3u8 → 解析失败 → 无限重试(日志:同一 m3u8
	// 请求 4 次全 200 但 ERROR_CODE_IO_NETWORK_CONNECTION_FAILED)。
	var rewrittenBody []byte
	if isM3U8Response(resp) {
		if body, err := io.ReadAll(resp.Body); err == nil {
			// 2026-08-06: 分片重写为 path-prefix 代理 URL（http://127.0.0.1:<port>/<target>/<path>），
			// 让系统 MediaPlayer（分片请求不带自定义 header）也能走 ECH 代理。
			// ExoPlayer/MPV 拿到该 URL 同样走代理（EchInterceptor 放行 127.0.0.1 直连本地代理 → path-prefix 识别 target）。
			proxyBase := "http://127.0.0.1" + s.cfg.Listen[strings.LastIndex(s.cfg.Listen, ":"):]
			rewrittenBody = rewriteM3U8(body, target, r.URL.Path, proxyBase)
			if !bytes.Equal(rewrittenBody, body) {
				log.Printf("[http] app-layer m3u8 rewritten for %s%s (%d -> %d bytes)", target, r.URL.Path, len(body), len(rewrittenBody))
			}
		}
	}

	for k, vv := range resp.Header {
		if hopByHopHeader(k) {
			continue
		}
		if resp.Uncompressed && (k == "Content-Encoding" || k == "Content-Length") {
			continue
		}
		if k == "Set-Cookie" {
			// WebView 页面 origin 是 http://127.0.0.1:<port>(代理地址),而
			// AO3/CF 的 Set-Cookie 带 Domain=archiveofourown.org + Secure
			// (+SameSite=None 必须配 Secure)→ 浏览器按跨域拒收 / Secure
			// cookie 不在 http 页面发送 → cf_clearance/session 进不了
			// WebView → 登录 POST 无验证凭据 → AO3 302 auth_error(实测)。
			// 改写: 去掉 Domain(变 host-only 按 127.0.0.1 存) + 去掉 Secure
			// + SameSite=None→Lax(http 页面可发送可收)。Go jar 不受影响:
			// jar 在 client.Do 内部已按原始 Set-Cookie 存好(archiveofourown.org 域)。
			for _, v := range vv {
				w.Header().Add(k, rewriteCookieForWebView(v))
			}
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if rewrittenBody != nil {
		// 重写后长度可能变化:必须用最终长度覆盖(在 WriteHeader 之前设置才有效)。
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(rewrittenBody)))
	}
	w.WriteHeader(resp.StatusCode)

	if rewrittenBody != nil {
		_, _ = w.Write(rewrittenBody)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// rewriteCookieForWebView 把上游 Set-Cookie 改写成 WebView(127.0.0.1 页面)
// 能收能发的形式: 去掉 Domain(host-only,按页面域 127.0.0.1 存)、去掉
// Secure、SameSite=None→Lax(SameSite=None 强制要求 Secure,不改成 http
// 页面会拒收)。值/Expires/Max-Age/Path/HttpOnly 保留。
func rewriteCookieForWebView(sc string) string {
	sc = regexp.MustCompile(`(?i);\s*Domain=[^;]*`).ReplaceAllString(sc, "")
	sc = regexp.MustCompile(`(?i);\s*Secure\b`).ReplaceAllString(sc, "")
	sc = regexp.MustCompile(`(?i);\s*SameSite=None`).ReplaceAllString(sc, "; SameSite=Lax")
	return sc
}

// resolveTarget determines the upstream host for an app-layer request.
// Returns (target, path, ok). Two modes:
//
//   - X-Ech-Target header (ExoPlayer/MPV initial request): target from header;
//     if the path also carries the path-prefix form (/<target>/<path>) — MPV's
//     global http-header-fields sends the header on EVERY request including
//     path-prefix segment URLs — strip the duplicated prefix so the upstream
//     request is https://<target>/<path>, not https://<target>/<target>/<path>.
//   - path-prefix only (segment requests from players that can't send headers):
//     target = first path segment (must look like a hostname, i.e. contain a
//     dot), path = remainder.
func resolveTarget(headerTarget, path string) (string, string, bool) {
	target := strings.TrimSpace(headerTarget)
	if target != "" {
		// Path-prefix URL with header also present: /<target>/<path> -> strip.
		prefix := "/" + strings.ToLower(target) + "/"
		if strings.HasPrefix(strings.ToLower(path), prefix) {
			path = path[len(prefix)-1:] // keep leading "/"
		}
		return target, path, true
	}
	// No header: try path-prefix form.
	if strings.HasPrefix(path, "/") {
		rest := strings.TrimPrefix(path, "/")
		if idx := strings.IndexByte(rest, '/'); idx > 0 {
			candidate := rest[:idx]
			// Require a dot: distinguishes a hostname from a normal path segment
			// like /watch/... or /video/... which are not path-prefix targets.
			if strings.Contains(candidate, ".") && validHost(candidate) {
				return strings.ToLower(candidate), rest[idx:], true
			}
		}
	}
	return "", "", false
}

// isM3U8Response reports whether an upstream response carries an HLS playlist:
// either an explicit mpegurl Content-Type or a .m3u8 URL suffix.
func isM3U8Response(resp *http.Response) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "vnd.apple.mpegurl") {
		return true
	}
	u := resp.Request.URL
	return u != nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// hopByHopHeader reports whether a header is restricted to a single hop.
func hopByHopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Proxy-Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// rewriteLocation maps an upstream absolute/relative Location back to a URL
// the app can follow through the same proxy.
func rewriteLocation(loc, target string) string {
	u, err := url.Parse(loc)
	if err != nil || !u.IsAbs() {
		return loc
	}
	u.Host = target
	return u.String()
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
		// 目标自身 HTTPS 记录无 ech= 时走完整获取链兜底
		// (磁盘缓存 → cloudflare-ech.com CF 通用公钥 → 目标自身 ech=),
		// 避免 CF 托管站点因未发布 ech= 而静默降级 plain TLS 泄漏 SNI。
		// ⚠️ 仅当目标解析到 AS13335(Cloudflare) 地址时才强注 CF 公钥;
		// 非 CF 主机(如只支持 TLS 1.2 的日本服务器)强注 ECH 会强制
		// TLS 1.3 握手导致本可成功的连接失败。
		if result.ECH == nil || len(result.ECH.Config) == 0 {
			as13335 := false
			for _, ip := range result.IPs {
				if cloudflare.IsAS13335(ip.String()) {
					as13335 = true
					break
				}
			}
			if as13335 {
				if b, outer, ferr := s.resolver.FetchECHConfig(host); ferr == nil && len(b) > 0 {
					result.ECH = &dns.ECHConfig{Config: b}
					if outer != "" {
						result.OuterSNI = outer
					}
					log.Printf("[socks5] ECH config for %s from fallback chain (outer=%s, len=%d)", host, outer, len(b))
				}
			}
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

// validHost accepts lowercase DNS host names only (no ports/paths/IPs/special
// chars). It mirrors the old upstream validation to keep per-host routing safe.
func validHost(value string) bool {
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil || strings.ContainsAny(value, "/:@?#\\") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(value, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return false
			}
		}
	}
	return true
}
