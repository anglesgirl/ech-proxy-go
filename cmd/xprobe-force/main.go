// i.pximg.net 强注强改探测（2026-08-16）：连 CF 边缘 + ECH（inner=i.pximg.net）
// 用法: go run ./cmd/xprobe-force
package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/dns"
	"github.com/anglesgirl/ech-proxy-go/internal/tlsconn"
	utls "github.com/refraction-networking/utls"
)

func main() {
	ips := []string{"172.64.146.66", "104.18.41.190", "162.159.36.5", "104.20.8.2"}
	inner := "i.pximg.net"

	resolver := dns.NewWithCache("https://pieqllv9i7.cloudflare-gateway.com/dns-query", 10*time.Second, 300*time.Second, "/tmp/xech-cache.json")

	// CF 公共 ECH 配置（cloudflare-ech.com）
	cfg, outer, err := resolver.FetchECHConfig("cloudflare-ech.com")
	if err != nil || len(cfg) == 0 {
		fmt.Println("拉 CF 公共 ECH 失败:", err)
		return
	}
	fmt.Printf("CF 公共 ECH %dB outer=%s, inner=%s\n\n", len(cfg), outer, inner)

	for _, ip := range ips {
		start := time.Now()
		res := &dns.Result{
			ECH:      &dns.ECHConfig{Config: cfg},
			OuterSNI: outer,
			IPs:      []net.IP{net.ParseIP(ip)},
		}
		d := tlsconn.New(5*time.Second, false, true)
		conn, err := d.DialECH(inner, res)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			fmt.Printf("%-16s ECH 失败: %v (%dms)\n", ip, err, ms)
			continue
		}
		accepted := false
		certCN := "(none)"
		if uc, ok := conn.(*utls.UConn); ok {
			st := uc.ConnectionState()
			accepted = st.ECHAccepted
			if pcs := st.PeerCertificates; len(pcs) > 0 {
				certCN = pcs[0].Subject.CommonName
			}
		} else if tc, ok := conn.(*tls.Conn); ok {
			st := tc.ConnectionState()
			accepted = st.ECHAccepted
			if pcs := st.PeerCertificates; len(pcs) > 0 {
				certCN = pcs[0].Subject.CommonName
			}
		}
		fmt.Printf("%-16s ECH=%v cert=%s (%dms)\n", ip, accepted, certCN, ms)
		conn.Close()
	}
}
