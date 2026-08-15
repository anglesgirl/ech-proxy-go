package echdoh

import (
	"net"
	"sync"
	"testing"
	"time"
)

// 核心回归：官方 IP 的 ECH 探测全失败时，绝不能把这批 IP 原样返回
// （2026-08-15 x.com code=37 根因：旧代码 fallback raw official，
// 把刚判定 ECH 不可用的 172.66.0.227 喂回 Firefox → TCP/TLS 超时）。
func TestForcedHintIPsNeverReturnsFailedOfficial(t *testing.T) {
	resetForcedHintState()

	const name = "x.com"
	official := []string{"172.66.0.227", "162.159.140.229"}

	// 预置探测缓存：可达池 IP 全 true（模拟 xprobe 实测 CF 边缘 ECH 可用），
	// 官方 IP 全 false（模拟 ECH 握手挂起）。官方 IP 在最后 seed，因为
	// fetchDohEndpointIPv4s 的内置快照里本身就含 162.159.140.229，
	// 必须让「官方=false」的判定覆盖池子里的同一个 IP。
	pool := fetchDohEndpointIPv4s()
	if len(pool) == 0 {
		t.Skip("no reachable pool IPs available in this environment")
	}
	seedECHCache(name, pool, true)
	// 官方 IP 的 /24 段采样也一律 false，避免真发网络请求
	seedSubnet24(name, official, false)
	seedECHCache(name, official, false)

	got := forcedHintIPs(name, official, 6)
	if len(got) == 0 {
		t.Fatal("forcedHintIPs returned empty — Firefox would loadError immediately")
	}
	for _, ip := range got {
		for _, bad := range official {
			if ip == bad {
				t.Errorf("returned ECH-failed official IP %s (regression)", ip)
			}
		}
	}
	t.Logf("forcedHintIPs -> %v", got)
}

// 官方 IP 有 ECH 可用的时候优先用官方段。
func TestForcedHintIPsPrefersOfficialWhenECHWorks(t *testing.T) {
	resetForcedHintState()

	const name = "example-forcedhint.test"
	official := []string{"104.17.16.197"}
	seedECHCache(name, official, true)
	seedSubnet24(name, official, false)

	got := forcedHintIPs(name, official, 6)
	if len(got) == 0 || got[0] != official[0] {
		t.Errorf("expected official ECH-OK IP first, got %v", got)
	}
}

// 结果缓存：同域名二次调用命中缓存（不重复探测）。
func TestForcedHintIPsCaches(t *testing.T) {
	resetForcedHintState()

	const name = "cache-forcedhint.test"
	official := []string{"104.17.16.197"}
	seedECHCache(name, official, true)
	seedSubnet24(name, official, false)

	first := forcedHintIPs(name, official, 6)
	// 把缓存里的探测结果翻成 false —— 若二次调用重新探测，结果会变
	seedECHCache(name, official, false)
	second := forcedHintIPs(name, official, 6)

	if len(first) == 0 || len(second) == 0 || first[0] != second[0] {
		t.Errorf("cache miss: first=%v second=%v", first, second)
	}
}

// ── 测试辅助 ──────────────────────────────────────────────

func resetForcedHintState() {
	forcedHintMu.Lock()
	forcedHintCache = map[string]forcedHintEntry{}
	forcedHintFlight = map[string]*sync.WaitGroup{}
	forcedHintMu.Unlock()
	echTestMu.Lock()
	echTestCache = map[string]echTestEntry{}
	echTestMu.Unlock()
	if len(upstream) == 0 {
		upstream = []string{"https://pieqllv9i7.cloudflare-gateway.com/dns-query"}
	}
}

// seedECHCache 预置 ECH 握手探测结果，避免测试发真实网络请求。
func seedECHCache(host string, ips []string, ok bool) {
	echTestMu.Lock()
	defer echTestMu.Unlock()
	for _, ip := range ips {
		echTestCache[host+"|"+ip] = echTestEntry{ok: ok, ts: time.Now()}
	}
}

// seedSubnet24 预置 official IP 所在 /24 段全部 254 个地址的探测结果，
// 让 officialSubnetIPs 的随机采样不会真去握手。
func seedSubnet24(host string, official []string, ok bool) {
	echTestMu.Lock()
	defer echTestMu.Unlock()
	for _, o := range official {
		v4 := net.ParseIP(o).To4()
		if v4 == nil {
			continue
		}
		for i := 1; i < 255; i++ {
			ip := net.IPv4(v4[0], v4[1], v4[2], byte(i)).String()
			key := host + "|" + ip
			if _, exists := echTestCache[key]; !exists {
				echTestCache[key] = echTestEntry{ok: ok, ts: time.Now()}
			}
		}
	}
}
