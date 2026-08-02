// ECH Proxy — 通用 ECH 前置代理
//
// 支持 HTTP CONNECT 和 SOCKS5 代理协议
// 内部通过 DoH 查询 DNS HTTPS 记录获取 ECHConfig
// 使用 Go 1.23+ crypto/tls 原生 ECH 支持
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/proxy"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径 (默认使用内置配置)")
	flag.Parse()

	// 兼容旧的命令行参数: ech-proxy <port> <doh-url>
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 命令行参数覆盖（兼容旧版本）
	args := flag.Args()
	if len(args) >= 1 {
		cfg.Listen = "127.0.0.1:" + args[0]
	}
	if len(args) >= 2 {
		cfg.DoH = args[1]
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置无效: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("ECH Proxy %s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
	log.Printf("  Listen: %s", cfg.Listen)
	log.Printf("  DoH:    %s", cfg.DoH)
	log.Printf("  Mode:   %s", cfg.Mode)

	srv := proxy.New(cfg)

	// 信号处理：优雅关闭
	// Windows: 只支持 Ctrl+C (os.Interrupt)
	// Unix:    支持 SIGINT 和 SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		if runtime.GOOS == "windows" {
			signal.Notify(sigCh, os.Interrupt)
		} else {
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		}
		sig := <-sigCh
		log.Printf("收到信号 %v，正在关闭...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		srv.Shutdown(ctx)
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("代理服务退出: %v", err)
	}
}

// Version 编译时注入
var Version = "dev"
