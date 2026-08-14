// ech-doh — 本地 DoH 注入服务器（桌面/CLI 版）
//
// 复用 mobile/echdoh 库（与 Android 内嵌同一实现，避免代码漂移）。
// 用法：
//   ech-doh -listen 127.0.0.1:8443 -cert fullchain.pem -key key.pem \
//     -upstream https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anglesgirl/ech-proxy-go/mobile/echdoh"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "listen address")
	certFile := flag.String("cert", "", "TLS cert PEM file")
	keyFile := flag.String("key", "", "TLS key PEM file")
	upstreams := flag.String("upstream",
		"https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query",
		"comma-separated upstream DoH")
	flag.Parse()

	cert, err := os.ReadFile(*certFile)
	if err != nil {
		log.Fatalf("read cert: %v", err)
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		log.Fatalf("read key: %v", err)
	}

	if err := echdoh.Start(*listen, string(cert), string(key), *upstreams); err != nil {
		log.Fatalf("start: %v", err)
	}
	log.Printf("ech-doh listening %s (HTTPS), upstream: %s", *listen, *upstreams)

	// 启动后 1.5s 打印服务内部状态（端口是否真正监听）
	time.Sleep(1500 * time.Millisecond)
	log.Printf("running=%v lastErr=%q", echdoh.IsRunning(), echdoh.LastError())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	_ = echdoh.Stop()
}
