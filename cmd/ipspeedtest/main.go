package main

import (
	"fmt"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/cloudflare"
)

func main() {
	start := time.Now()
	ips := cloudflare.OptimizeFastIPs("/tmp/ipspeedtest_cache")
	fmt.Printf("结果: %d 个最快 IP: %v (耗时 %v)\n", len(ips), ips, time.Since(start))
	for _, ip := range ips {
		fmt.Printf("  %s AS13335=%v\n", ip, cloudflare.IsAS13335(ip))
	}
}
