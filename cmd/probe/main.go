package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/proxy"
)

func main() {
	cfg := &config.Config{
		Listen: "127.0.0.1:18999",
		Mode:   "http",
		DoH:    "https://pieqllv9i7.cloudflare-gateway.com/dns-query",
	}
	cfg.Proxy.ConnectTimeout = "15s"
	cfg.Proxy.IdleTimeout = "60s"
	cfg.DNS.Timeout = "10s"
	cfg.DNS.CacheTTL = "300s"
	cfg.TLS.Timeout = "15s"
	cfg.TLS.FallbackPlain = true

	srv := proxy.New(cfg)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			fmt.Println("listen err:", err)
		}
	}()
	time.Sleep(6 * time.Second)

	client := &http.Client{Timeout: 25 * time.Second}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:18999/video/m3u8/2025/11/28/9401e253/index.m3u8", nil)
	req.Header.Set("X-Ech-Target", "t33.cdn2020.com")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/126.0 Mobile")
	req.Header.Set("Referer", "https://javchu.com/")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("req err:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 30000))
	fmt.Printf("HTTP %d len=%d CT=%s\n", resp.StatusCode, len(body), resp.Header.Get("Content-Type"))
	fmt.Println(string(body))
}
