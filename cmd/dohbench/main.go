// dohbench — 在 Firefox 的真实时间预算内测量 echdoh 的应答延迟。
//
// 为什么需要这个工具（2026-08-15 echbrowser x.com code=37 第二形态）：
//
// xprobe 一直报 x.com HTTP 200，但 echbrowser 里 Firefox 直接 loadError。
// 两者的差别不是「能不能连通」，而是「有没有时间预算」：
//
//	19:31:09.978  PAGE start=https://x.com/
//	19:31:17.193  ❌ loadError code=37          ← Firefox 放弃
//	19:31:17.291  forced hint IPs <- reachable-pool(ECH-probed) ✓
//	19:31:17.293  x.com A -> 6 answers          ← 晚了 98ms，IP 全对
//
// Firefox 的 network.trr.request-timeout 默认 3000ms，且 trr.mode=3
// （TRR-only）没有 Do53 回退 —— DNS 答不出来就是直接失败。而 echdoh 的
// forcedHintIPs() 把 ECH 握手探测放在 DNS 响应路径里同步做，冷启动要
// 5~7s。探测结果再准确也没人要了。
//
// 这个工具不测「墙」，只测「延迟」——所以在任何网络环境下跑都有意义，
// 服务器上跑出的数字和手机上的量级一致（探测耗时来自逻辑本身）。
//
// 用法：
//
//	go run ./cmd/dohbench                          # 默认测 x.com 全家桶
//	go run ./cmd/dohbench -hosts x.com,pbs.twimg.com
//	go run ./cmd/dohbench -budget 3000 -rounds 3   # 自定义预算/轮次
//
// 判定标准：任一查询超过 budget 即视为 Firefox 侧失败（TIMEOUT），
// 因为 trr.mode=3 无回退。A 记录答案为空同样是失败（Firefox 立即
// loadError，不会等后续探测）。
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/mobile/echdoh"
	"github.com/miekg/dns"
)

const defaultHosts = "x.com,www.x.com,api.x.com,abs.twimg.com,pbs.twimg.com,video.twimg.com"

func main() {
	var (
		listen    = flag.String("listen", "127.0.0.1:18443", "本地 echdoh 监听地址")
		upstreams = flag.String("upstreams", "https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query", "上游 DoH（逗号分隔）")
		hostsFlag = flag.String("hosts", defaultHosts, "测试域名（逗号分隔）")
		budgetMs  = flag.Int("budget", 3000, "单查询预算 ms（Firefox network.trr.request-timeout 默认 3000）")
		rounds    = flag.Int("rounds", 2, "轮次（第 1 轮=冷启动，后续=热缓存）")
		warmWait  = flag.Duration("warmup", 0, "启动后等待多久再开测（模拟 App 预热窗口）")
		verbose   = flag.Bool("v", false, "打印 echdoh 内部日志")
	)
	flag.Parse()

	budget := time.Duration(*budgetMs) * time.Millisecond
	hosts := splitTrim(*hostsFlag)
	if len(hosts) == 0 {
		fmt.Fprintln(os.Stderr, "no hosts")
		os.Exit(2)
	}

	certPEM, keyPEM, err := selfSignedCert()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen cert: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== dohbench ===\n")
	fmt.Printf("listen=%s budget=%v rounds=%d\n", *listen, budget, *rounds)
	fmt.Printf("upstreams=%s\n", *upstreams)
	fmt.Printf("hosts=%s\n\n", strings.Join(hosts, ","))

	t0 := time.Now()
	if err := echdoh.Start(*listen, certPEM, keyPEM, *upstreams); err != nil {
		fmt.Fprintf(os.Stderr, "echdoh.Start: %v\n", err)
		os.Exit(1)
	}
	defer echdoh.Stop()
	if err := waitReady(*listen, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "server not ready: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("server ready in %v\n", time.Since(t0).Round(time.Millisecond))

	if *warmWait > 0 {
		fmt.Printf("warmup wait %v...\n", *warmWait)
		time.Sleep(*warmWait)
	}
	fmt.Println()

	client := newDoHClient(*listen)
	var all []hostReport

	for r := 1; r <= *rounds; r++ {
		label := "热缓存"
		if r == 1 {
			label = "冷启动"
		}
		fmt.Printf("───── 第 %d 轮（%s）─────\n", r, label)
		printHeader()
		for _, h := range hosts {
			rep := benchHost(client, h, budget)
			rep.round = r
			all = append(all, rep)
			printRow(rep, budget)
		}
		fmt.Println()
		if *verbose {
			if logs := echdoh.PollLogs(); logs != "" {
				fmt.Println("--- echdoh 日志 ---")
				fmt.Println(logs)
			}
		}
	}

	summarize(all, budget, hosts, *rounds)
}

// ── 单域名测量 ────────────────────────────────────────────

// hostReport 一个域名一轮的测量结果。
type hostReport struct {
	round int
	host  string
	// Firefox 会几乎同时发这三个查询，各自独立计时
	a, aaaa, https queryResult
}

type queryResult struct {
	qtype   string
	elapsed time.Duration
	answers int
	timeout bool // 超过预算
	err     error
	detail  string // A: IP 列表；HTTPS: ech 长度 + ipv4hint
}

// benchHost 并发发 A / AAAA / HTTPS 三个查询（与 Firefox 行为一致），
// 每个查询独立 deadline = budget。
func benchHost(c *dohClient, host string, budget time.Duration) hostReport {
	rep := hostReport{host: host}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); rep.a = doQuery(c, host, dns.TypeA, budget) }()
	go func() { defer wg.Done(); rep.aaaa = doQuery(c, host, dns.TypeAAAA, budget) }()
	go func() { defer wg.Done(); rep.https = doQuery(c, host, dns.TypeHTTPS, budget) }()
	wg.Wait()
	return rep
}

func doQuery(c *dohClient, host string, qtype uint16, budget time.Duration) queryResult {
	res := queryResult{qtype: dns.TypeToString[qtype]}

	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), qtype)
	m.RecursionDesired = true
	wire, err := m.Pack()
	if err != nil {
		res.err = err
		return res
	}

	start := time.Now()
	req, err := http.NewRequest("POST", c.url, bytes.NewReader(wire))
	if err != nil {
		res.err = err
		return res
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := c.http.Do(req)
	res.elapsed = time.Since(start)
	if err != nil {
		res.err = err
		if res.elapsed >= budget {
			res.timeout = true
		}
		return res
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	res.elapsed = time.Since(start)
	if err != nil {
		res.err = err
		return res
	}
	if res.elapsed >= budget {
		res.timeout = true
	}

	out := new(dns.Msg)
	if err := out.Unpack(body); err != nil {
		res.err = fmt.Errorf("unpack: %w", err)
		return res
	}
	res.answers = len(out.Answer)
	res.detail = describeAnswers(out, qtype)
	return res
}

func describeAnswers(m *dns.Msg, qtype uint16) string {
	switch qtype {
	case dns.TypeA:
		var ips []string
		for _, rr := range m.Answer {
			if a, ok := rr.(*dns.A); ok {
				ips = append(ips, a.A.String())
			}
		}
		return strings.Join(ips, " ")
	case dns.TypeAAAA:
		var ips []string
		for _, rr := range m.Answer {
			if a, ok := rr.(*dns.AAAA); ok {
				ips = append(ips, a.AAAA.String())
			}
		}
		if len(ips) == 0 {
			return "(empty, forced IPv4)"
		}
		return strings.Join(ips, " ")
	case dns.TypeHTTPS:
		var parts []string
		for _, rr := range m.Answer {
			// ⚠️ HTTPS 记录 unpack 后是 *dns.HTTPS（内嵌 SVCB），不是
			// *dns.SVCB。只判断 *dns.SVCB 会漏掉全部真实响应。
			var vals []dns.SVCBKeyValue
			switch v := rr.(type) {
			case *dns.HTTPS:
				vals = v.Value
			case *dns.SVCB:
				vals = v.Value
			default:
				continue
			}
			for _, kv := range vals {
				switch v := kv.(type) {
				case *dns.SVCBECHConfig:
					parts = append(parts, fmt.Sprintf("ech=%dB", len(v.ECH)))
				case *dns.SVCBIPv4Hint:
					var hs []string
					for _, ip := range v.Hint {
						hs = append(hs, ip.String())
					}
					parts = append(parts, "hint="+strings.Join(hs, ","))
				case *dns.SVCBAlpn:
					parts = append(parts, "alpn="+strings.Join(v.Alpn, "/"))
				}
			}
		}
		if len(parts) == 0 {
			return "(no svcb)"
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// ── 输出 ──────────────────────────────────────────────────

func printHeader() {
	fmt.Printf("%-20s %-6s %9s  %-7s %s\n", "HOST", "TYPE", "延迟", "判定", "答案")
}

func printRow(rep hostReport, budget time.Duration) {
	for _, q := range []queryResult{rep.a, rep.aaaa, rep.https} {
		verdict, detail := verdictOf(q, budget)
		fmt.Printf("%-20s %-6s %9s  %-7s %s\n",
			rep.host, q.qtype, q.elapsed.Round(time.Millisecond), verdict, detail)
	}
}

// verdictOf 判定单个查询：Firefox trr.mode=3 无回退，超预算或 A 记录空
// 都会导致 loadError。
func verdictOf(q queryResult, budget time.Duration) (string, string) {
	switch {
	case q.err != nil:
		return "ERR", q.err.Error()
	case q.timeout:
		return "TIMEOUT", fmt.Sprintf("超预算 %v（Firefox 已放弃）%s",
			budget, prefixSpace(q.detail))
	case q.qtype == "A" && q.answers == 0:
		return "EMPTY", "A 记录为空 → Firefox 立即 loadError"
	default:
		return "OK", q.detail
	}
}

func prefixSpace(s string) string {
	if s == "" {
		return ""
	}
	return " · " + s
}

func summarize(all []hostReport, budget time.Duration, hosts []string, rounds int) {
	fmt.Println("═════ 汇总 ═════")

	type stat struct {
		maxCold  time.Duration
		maxWarm  time.Duration
		failCold bool
		failWarm bool
	}
	byHost := map[string]*stat{}
	for _, rep := range all {
		s := byHost[rep.host]
		if s == nil {
			s = &stat{}
			byHost[rep.host] = s
		}
		for _, q := range []queryResult{rep.a, rep.aaaa, rep.https} {
			v, _ := verdictOf(q, budget)
			bad := v != "OK"
			if rep.round == 1 {
				if q.elapsed > s.maxCold {
					s.maxCold = q.elapsed
				}
				s.failCold = s.failCold || bad
			} else {
				if q.elapsed > s.maxWarm {
					s.maxWarm = q.elapsed
				}
				s.failWarm = s.failWarm || bad
			}
		}
	}

	fmt.Printf("%-20s %12s %12s  %s\n", "HOST", "冷启动最慢", "热缓存最慢", "结论")
	var coldFails, warmFails []string
	for _, h := range hosts {
		s := byHost[h]
		if s == nil {
			continue
		}
		concl := "两轮都在预算内 ✓"
		switch {
		case s.failCold && s.failWarm:
			concl = "冷热都失败 ✗"
			coldFails = append(coldFails, h)
			warmFails = append(warmFails, h)
		case s.failCold:
			concl = "冷启动失败、热缓存 OK（首次打开必挂）"
			coldFails = append(coldFails, h)
		case s.failWarm:
			concl = "热缓存失败 ✗"
			warmFails = append(warmFails, h)
		}
		warm := s.maxWarm.Round(time.Millisecond).String()
		if rounds < 2 {
			warm = "-"
		}
		fmt.Printf("%-20s %12s %12s  %s\n", h,
			s.maxCold.Round(time.Millisecond), warm, concl)
	}

	fmt.Println()
	sort.Strings(coldFails)
	switch {
	case len(coldFails) == 0 && len(warmFails) == 0:
		fmt.Printf("✅ 全部域名冷热两轮均在 %v 预算内应答 —— DNS 层不是瓶颈，\n", budget)
		fmt.Println("   Firefox code=37 的原因要往 TLS/ECH 连接层找。")
	case len(coldFails) > 0 && len(warmFails) == 0:
		fmt.Printf("❌ 冷启动超预算：%s\n", strings.Join(coldFails, " "))
		fmt.Printf("   根因 = ECH 探测在 DNS 响应路径上同步执行，冷启动无缓存可用。\n")
		fmt.Printf("   Firefox trr.mode=3 无 Do53 回退，超 %v 直接 loadError。\n", budget)
		fmt.Println("   方向：DNS 响应绝不阻塞探测 —— 先答已缓存/预热结果，")
		fmt.Println("   探测转后台只影响下一次查询（App 启动即预热 x.com 全家桶）。")
	default:
		fmt.Printf("❌ 冷启动失败：%s\n", strings.Join(coldFails, " "))
		fmt.Printf("❌ 热缓存仍失败：%s\n", strings.Join(warmFails, " "))
		fmt.Println("   热缓存也超预算说明缓存没生效或每次都在重新探测，先查缓存键/TTL。")
	}
}

// ── 基础设施 ──────────────────────────────────────────────

// dohClient 指向本地 echdoh 的 DoH 客户端。
type dohClient struct {
	url  string
	http *http.Client
}

func newDoHClient(addr string) *dohClient {
	return &dohClient{
		url: "https://" + addr + "/dns-query",
		// 不设 Timeout：预算判定靠实测 elapsed，超预算的查询仍然跑完，
		// 才能看出「到底慢多少」。
		http: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // 本地自签
				DisableKeepAlives: false,
			},
		},
	}
}

func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

func selfSignedCert() (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "dohbench-local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return string(certPEM), string(keyPEM), nil
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
