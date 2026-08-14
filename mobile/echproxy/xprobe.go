// XProbe — x.com 专用强制 ECH 连通性测试（gomobile 导出）
//
// 测试思路（用户拍板：直接强制）：
//  1. 所有 host 一律用 DoH 解析，无视系统 DNS（防污染指向）
//  2. 不管目标是否发布自己的 ech= 记录，一律强制灌入 Cloudflare 公共
//     ECH 公钥（cloudflare-ech.com / 内置快照），fallbackPlain=false
//     —— ECH 失败绝不降级明文（SNI 泄漏）
//  3. 连接候选 = DoH 解析 IP + 自动并入的 DoH 端点 IP（CF 边缘，可达）
//  4. 每个 host 报告：解析 IP / ECH accepted / HTTP 状态 / 耗时
package echproxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/dns"
	"github.com/anglesgirl/ech-proxy-go/internal/tlsconn"
	utls "github.com/refraction-networking/utls"
)

// XProbe 对 hosts（逗号分隔）逐一做强制 ECH 测试，返回多行文本报告。
// doh 为空时使用默认 Cloudflare Gateway 端点。
func XProbe(doh, hosts string) string {
	var out string
	_ = safe("XProbe", func() error {
		if strings.TrimSpace(doh) == "" {
			doh = "https://pieqllv9i7.cloudflare-gateway.com/dns-query"
		}
		out = xprobeRun(doh, hosts)
		return nil
	})
	return out
}

func xprobeRun(doh, hosts string) string {
	var list []string
	for _, h := range strings.Split(hosts, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			list = append(list, h)
		}
	}
	if len(list) == 0 {
		list = defaultXHosts()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "DoH: %s\n时间: %s\n\n", doh, time.Now().Format("15:04:05"))
	log.Printf("[xprobe] DoH: %s 时间: %s", doh, time.Now().Format("15:04:05"))

	resolver := dns.New(doh, 10*time.Second, 300*time.Second)
	dialer := tlsconn.New(20*time.Second, false, false) // fallbackPlain=false → 强制 ECH
	// 自动并入 DoH 端点 IP（CF 边缘，目标 IP 被封时兜底）
	if ips := xResolveDoHHostIPs(doh); len(ips) > 0 {
		dialer.AppendCustomIPs(ips)
		fmt.Fprintf(&b, "DoH端点IP兜底: %s\n", strings.Join(ips, ","))
		log.Printf("[xprobe] DoH端点IP兜底: %s", strings.Join(ips, ","))
	}

	fmt.Fprintf(&b, "%-20s %-20s %-11s %-6s %-8s %s\n",
		"HOST", "IP(DoH)", "ECH", "HTTP", "耗时", "结果")
	log.Printf("[xprobe] %-20s %-20s %-11s %-6s %-8s %s",
		"HOST", "IP(DoH)", "ECH", "HTTP", "耗时", "结果")

	anyOK := false
	for _, host := range list {
		line, ok := xTestHost(resolver, dialer, host)
		if ok {
			anyOK = true
		}
		b.WriteString(line)
		// 每测完一个 host 立即写入日志，Android 轮询实时可见
		log.Printf("[xprobe] %s", strings.TrimSuffix(line, "\n"))
	}

	b.WriteString("\n=== 汇总 ===\n")
	log.Printf("[xprobe] === 汇总 ===")
	if anyOK {
		b.WriteString("✅ 强制灌入 CF 公共 ECH 公钥 + DoH IP 直连 → 可访问\n")
		log.Printf("[xprobe] ✅ 强制灌入 CF 公共 ECH 公钥 + DoH IP 直连 → 可访问")
	} else {
		b.WriteString("❌ 全部失败（见上）\n")
		log.Printf("[xprobe] ❌ 全部失败（见上）")
	}
	return b.String()
}

// xprobeRunStreaming 与 xprobeRun 相同，但每步结果通过 log 实时输出
// （StartProbe 后台模式用，PollLogs 增量拉取）。
func xprobeRunStreaming(doh, hosts string) string {
	return xprobeRun(doh, hosts)
}

func xTestHost(resolver *dns.Resolver, dialer *tlsconn.Dialer, host string) (string, bool) {
	start := time.Now()
	pad := func(s string) string { return fmt.Sprintf("%-20s", s) }

	// 1. DoH 解析（强制，无视系统 DNS）
	result, err := resolver.Lookup(host, true)
	if err != nil || len(result.IPs) == 0 {
		return fmt.Sprintf("%s %s %s %-6s %-8s %s\n",
			pad(host), pad("解析失败"), pad("-"), "-", "-", err), false
	}
	var ipParts []string
	for _, ip := range result.IPs {
		ipParts = append(ipParts, ip.String())
	}
	ipStr := strings.Join(ipParts, ",")

	// 2. 强制灌入 ECH（disk→cloudflare-ech.com→内置公钥）
	echConfig, outer, err := resolver.FetchECHConfig(host)
	if err != nil || len(echConfig) == 0 {
		return fmt.Sprintf("%s %s %s %-6s %-8s %s\n",
			pad(host), pad(ipStr), pad("无公钥"), "-", time.Since(start).Round(time.Millisecond),
			"FetchECHConfig: "+err.Error()), false
	}
	result.ECH = &dns.ECHConfig{Config: echConfig}
	if outer != "" {
		result.OuterSNI = outer
	}

	// 3. ECH 握手（强制，无降级）
	conn, err := dialer.DialECH(host, result)
	if err != nil {
		return fmt.Sprintf("%s %s %s %-6s %-8s %s\n",
			pad(host), pad(ipStr), pad("拒绝"), "-", time.Since(start).Round(time.Millisecond),
			"ECH握手: "+err.Error()), false
	}
	defer conn.Close()

	echAccepted := "⚠️未接受"
	if ok, has := xECHState(conn); has && ok {
		echAccepted = "✅accepted"
	}

	// 4. 复用 TLS 连接发 HTTPS 请求
	statusCode, respLen, bodyPreview, err := xRequest(conn, host, "/", 20*time.Second)
	if err != nil {
		return fmt.Sprintf("%s %s %s %-6s %-8s %s\n",
			pad(host), pad(ipStr), pad(echAccepted), "-", time.Since(start).Round(time.Millisecond),
			"HTTP: "+err.Error()), false
	}
	ok := statusCode >= 200 && statusCode < 400
	line := fmt.Sprintf("%s %s %s %-6d %-8s HTTP %d, body %d bytes\n",
		pad(host), pad(ipStr), pad(echAccepted), statusCode,
		time.Since(start).Round(time.Millisecond), statusCode, respLen)
	// 非 2xx/3xx 时附响应 body 预览（判断 403 是 geo block 还是 bot 检测）
	if !ok && bodyPreview != "" {
		line += fmt.Sprintf("%s     └─ body: %s\n", pad(""), truncStr(bodyPreview, 300))
	}
	return line, ok
}

// xECHState 读取连接 ECH accepted 状态（utls.UConn 或 crypto/tls）。
func xECHState(conn net.Conn) (bool, bool) {
	if tc, ok := conn.(*tls.Conn); ok {
		return tc.ConnectionState().ECHAccepted, true
	}
	if uc, ok := conn.(*utls.UConn); ok {
		return uc.ConnectionState().ECHAccepted, true
	}
	return false, false
}

// xRequest 在已握手 TLS 连接上发 HTTP/1.1 GET。
// 返回：状态码, body 字节数, 响应头+body 前 800 字节预览（诊断 403 用）。
func xRequest(conn net.Conn, host, path string, timeout time.Duration) (int, int, string, error) {
	conn.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36\r\nAccept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\nAccept-Language: en-US,en;q=0.9\r\nConnection: close\r\n\r\n", path, host)
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, 0, "", err
	}
	body, err := io.ReadAll(io.LimitReader(conn, 256*1024))
	if err != nil && len(body) == 0 {
		return 0, 0, "", err
	}
	head := string(body)
	if len(head) > 4096 {
		head = head[:4096]
	}
	statusCode := 0
	lines := strings.SplitN(head, "\r\n", 3)
	if len(lines) > 0 && strings.HasPrefix(lines[0], "HTTP/") {
		fmt.Sscanf(lines[0], "HTTP/%d.%d %d", new(int), new(int), &statusCode)
	}
	// 预览 = 响应头（前几行）+ body 开头，便于判断 CF 拦截类型
	preview := ""
	if len(lines) >= 3 {
		headers := lines[1]
		bodyStart := lines[2]
		// 提取关键 CF 头
		var cfHits []string
		for _, h := range strings.Split(headers, "\r\n") {
			l := strings.ToLower(h)
			if strings.Contains(l, "cf-ray") || strings.Contains(l, "cf-mitigated") ||
				strings.Contains(l, "server") || strings.Contains(l, "cf-chl") ||
				strings.Contains(l, "set-cookie") && strings.Contains(l, "cf_clearance") {
				cfHits = append(cfHits, h)
			}
		}
		if len(cfHits) > 0 {
			preview = strings.Join(cfHits, " | ") + " || "
		}
		preview += strings.TrimSpace(bodyStart)
	}
	if len(preview) > 800 {
		preview = preview[:800]
	}
	return statusCode, len(body), preview, nil
}

// xResolveDoHHostIPs 解析 DoH 端点域名 IP（系统 DNS 可用时用，失败用内置快照）。
func xResolveDoHHostIPs(dohURL string) []string {
	host := strings.TrimPrefix(strings.TrimPrefix(dohURL, "https://"), "http://")
	if i := strings.Index(host, "/"); i > 0 {
		host = host[:i]
	}
	var ips []string
	if addrs, err := net.LookupHost(host); err == nil {
		for _, a := range addrs {
			if net.ParseIP(a) != nil {
				ips = append(ips, a)
			}
		}
	}
	for _, b := range []string{"162.159.36.5", "162.159.36.20"} {
		found := false
		for _, a := range ips {
			if a == b {
				found = true
				break
			}
		}
		if !found {
			ips = append(ips, b)
		}
	}
	return ips
}

func defaultXHosts() []string {
	return []string{
		"x.com",
		"www.x.com",
		"api.x.com",
		"video.twimg.com",
		"abs.twimg.com",
		"pbs.twimg.com",
	}
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
