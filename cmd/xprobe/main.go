// x.com 专用 ECH 测试浏览器 (CLI)
//
// 思路（用户拍板：直接强制）：
//   1. 所有 x.com 相关域名一律用 DoH 解析（不走系统 DNS，无视污染指向）
//   2. 不管目标是否发布自己的 ech= 记录，一律强制灌入 Cloudflare 公共
//      ECH 公钥（cloudflare-ech.com / 内置快照），fallbackPlain=false
//      —— ECH 失败绝不降级明文（SNI 会泄漏）
//   3. 连接候选 = DoH 解析出的 CF IP（+ 自动并入的 DoH 端点 IP）
//   4. 逐个域名报告：解析 IP / ECH accepted / HTTP 状态码 / 耗时
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/anglesgirl/ech-proxy-go/internal/dns"
	"github.com/anglesgirl/ech-proxy-go/internal/tlsconn"
)

// connState 读取 utls.UConn 或 *tls.Conn 的 ECH accepted 状态。
func connState(conn net.Conn) (bool, bool) {
	if tc, ok := conn.(*tls.Conn); ok {
		return tc.ConnectionState().ECHAccepted, true
	}
	if uc, ok := conn.(*utls.UConn); ok {
		return uc.ConnectionState().ECHAccepted, true
	}
	return false, false
}

var _ = utls.HelloChrome_Auto

var defaultHosts = []string{
	"x.com",
	"www.x.com",
	"api.x.com",
	"video.twimg.com",
	"upload.twimg.com",
	"abs.twimg.com",
	"pbs.twimg.com",
}

func main() {
	doh := flag.String("doh", "https://pieqllv9i7.cloudflare-gateway.com/dns-query", "DoH endpoint")
	hostsFlag := flag.String("hosts", "", "comma-separated hosts (default: x.com family)")
	path := flag.String("path", "/", "request path")
	timeout := flag.Duration("timeout", 20*time.Second, "per-host timeout")
	noDowngrade := flag.Bool("no-downgrade", true, "never fall back to plain TLS (default true = 强制 ECH)")
	customIPs := flag.String("ip", "", "comma-separated CF edge IPs to force (optional)")
	flag.Parse()

	var hosts []string
	if *hostsFlag != "" {
		hosts = strings.Split(*hostsFlag, ",")
	} else {
		hosts = defaultHosts
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 解析器：只走 DoH，任何情况不碰系统 DNS
	resolver := dns.NewWithCache(*doh, 10*time.Second, 300*time.Second, "/tmp/xech-cache.json")
	dialer := tlsconn.New(*timeout, false, !*noDowngrade)
	if *customIPs != "" {
		dialer.SetCustomIPs(*customIPs)
		fmt.Printf("强制候选 IP: %s\n", *customIPs)
	}
	// 自动并入 DoH 端点 IP（CF 边缘，可达）
	if ips := resolveDoHHostIPs(*doh); len(ips) > 0 {
		dialer.AppendCustomIPs(ips)
		fmt.Printf("自动并入 DoH 端点 IP: %s\n", strings.Join(ips, ","))
	}

	fmt.Printf("DoH: %s\n时间: %s\n\n", *doh, time.Now().Format("15:04:05"))
	fmt.Printf("%-22s %-22s %-12s %-7s %-9s %s\n", "HOST", "IP(DoH解析)", "ECH", "HTTP", "耗时", "结果")

	anyOK := false
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		report := testHost(resolver, dialer, host, *path, *timeout)
		if report.ok {
			anyOK = true
		}
		fmt.Println(report.line)
	}

	fmt.Println("\n=== 汇总 ===")
	if anyOK {
		fmt.Println("✅ 强制灌入 CF 公共 ECH 公钥 + DoH IP 直连 → x.com 可访问")
	} else {
		fmt.Println("❌ 全部失败（详见上面每行结果）")
	}
}

type report struct {
	line string
	ok   bool
}

func testHost(resolver *dns.Resolver, dialer *tlsconn.Dialer, host, path string, timeout time.Duration) report {
	start := time.Now()

	// 1. DoH 解析（强制，无视系统 DNS）
	result, err := resolver.Lookup(host, true)
	if err != nil || len(result.IPs) == 0 {
		return report{line: fmt.Sprintf("%-22s %-22s %-12s %-7s %-9s %s", host, "解析失败", "-", "-", "-", err), ok: false}
	}
	ipStr := result.IPs[0].String()
	for _, ip := range result.IPs[1:] {
		ipStr += "," + ip.String()
	}

	// 2. 强制灌入 ECH（disk→cloudflare-ech.com→内置公钥，与目标自身 ech= 无关）
	echConfig, outer, err := resolver.FetchECHConfig(host)
	if err != nil || len(echConfig) == 0 {
		return report{line: fmt.Sprintf("%-22s %-22s %-12s %-7s %-9s %s", host, ipStr, "无公钥", "-", time.Since(start).Round(time.Millisecond), "FetchECHConfig: "+err.Error()), ok: false}
	}
	result.ECH = &dns.ECHConfig{Config: echConfig}
	if outer != "" {
		result.OuterSNI = outer
	}

	// 3. ECH 握手（fallbackPlain=false 已由调用方保证）
	conn, err := dialer.DialECH(host, result)
	if err != nil {
		return report{line: fmt.Sprintf("%-22s %-22s %-12s %-7s %-9s %s", host, ipStr, "拒绝", "-", time.Since(start).Round(time.Millisecond), "ECH握手: "+err.Error()), ok: false}
	}

	// 读 ECH accepted 状态（utls.UConn 或 crypto/tls 的 *tls.Conn）
	echAccepted := "-"
	switch tc := conn.(type) {
	case *tls.Conn:
		if tc.ConnectionState().ECHAccepted {
			echAccepted = "✅accepted"
		} else {
			echAccepted = "⚠️未接受"
		}
	default:
		if accepted, ok := connState(conn); ok {
			if accepted {
				echAccepted = "✅accepted"
			} else {
				echAccepted = "⚠️未接受"
			}
		}
	}

	// 4. 复用该 TLS 连接发 HTTPS 请求
	statusCode, respLen, err := requestOverConn(conn, host, path, timeout)
	if err != nil {
		return report{line: fmt.Sprintf("%-22s %-22s %-12s %-7s %-9s %s", host, ipStr, echAccepted, "-", time.Since(start).Round(time.Millisecond), "HTTP: "+err.Error()), ok: false}
	}
	ok := statusCode >= 200 && statusCode < 400
	return report{
		line: fmt.Sprintf("%-22s %-22s %-12s %-7d %-9s %s", host, ipStr, echAccepted, statusCode, time.Since(start).Round(time.Millisecond), fmt.Sprintf("HTTP %d, body %d bytes", statusCode, respLen)),
		ok:   ok,
	}
}

// requestOverConn 在已握手的 TLS 连接上发 HTTP/1.1 GET 并读响应。
func requestOverConn(conn net.Conn, host, path string, timeout time.Duration) (int, int, error) {
	conn.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36\r\nAccept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\nAccept-Language: en-US,en;q=0.9\r\nConnection: close\r\n\r\n", path, host)
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, 0, err
	}

	body, err := io.ReadAll(io.LimitReader(conn, 256*1024))
	if err != nil {
		// 读到 EOF 或 deadline 都算拿到数据
		if len(body) == 0 {
			return 0, 0, err
		}
	}
	// 解析状态行
	head := string(body)
	if len(head) > 4096 {
		head = head[:4096]
	}
	statusCode := 0
	if i := strings.Index(head, " "); i > 0 {
		statusLine := strings.TrimSpace(head[:i])
		_ = statusLine
	}
	lines := strings.SplitN(head, "\r\n", 3)
	if len(lines) > 0 && strings.HasPrefix(lines[0], "HTTP/") {
		fmt.Sscanf(lines[0], "HTTP/%d.%d %d", new(int), new(int), &statusCode)
	}
	return statusCode, len(body), nil
}

// resolveDoHHostIPs 解析 DoH 端点域名 IP（系统 DNS 可用时用，失败用内置快照）
func resolveDoHHostIPs(dohURL string) []string {
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

var _ = os.Exit
