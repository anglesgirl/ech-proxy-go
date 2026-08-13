package tlsconn

import (
	"testing"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/dns"
)

// 逐个测福建相关 IP 的 utls ECH 握手
func TestUtlsECHOnFujianIPs(t *testing.T) {
	ips := []string{
		"104.18.41.190", // 用户 custom IP（福建被封？）
		"172.64.146.66", // 用户 custom IP
		"162.159.36.5",  // DoH 端点 IP（福建可达）
		"162.159.36.20", // DoH 端点 IP
		"104.20.9.2",    // 本机成功参考
	}
	for _, ip := range ips {
		d := New(15*time.Second, false, false)
		d.SetCustomIPs(ip)
		resolver := dns.New("https://pieqllv9i7.cloudflare-gateway.com/dns-query", 10*time.Second, 5*time.Minute)
		dc := NewWithCache(d, resolver)
		conn, err := dc.DialContext(nil, "tcp", "archiveofourown.org:443")
		if err != nil {
			t.Logf("IP %s: FAIL %v", ip, err)
			continue
		}
		conn.Close()
		t.Logf("IP %s: OK", ip)
	}
}
