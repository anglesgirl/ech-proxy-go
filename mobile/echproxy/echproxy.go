// Package echproxy exposes the reusable proxy as a small gomobile-friendly API.
//
// Android applications should call this package for lifecycle only. DNS, ECH,
// TLS retry/fallback, caching, and CONNECT handling remain in the internal
// packages owned by this repository.
//
// Every exported function recovers panics: a panic inside a gomobile-bound Go
// function aborts the whole Android process, which shows up as an endless
// app restart loop. Recovering and recording the error keeps the app alive.
package echproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/anglesgirl/ech-proxy-go/internal/cloudflare"
	"github.com/anglesgirl/ech-proxy-go/internal/config"
	"github.com/anglesgirl/ech-proxy-go/internal/proxy"
)

type boundedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *boundedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.b.Write(p)
	if l.b.Len() > 64*1024 {
		data := l.b.Bytes()
		l.b.Reset()
		_, _ = l.b.Write(data[len(data)-48*1024:])
	}
	return len(p), nil
}

func (l *boundedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var (
	mu          sync.Mutex
	server      *proxy.Server
	lastInfo    = "not started"
	logs        = &boundedLog{}
	mitmEnabled bool
)

// safe runs fn and converts any panic into a recorded status + error, so a
// gomobile JNI call never aborts the Android process.
func safe(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%s panic: %v\n%s", what, r, debug.Stack())
			mu.Lock()
			lastInfo = msg
			mu.Unlock()
			log.Print(msg)
			err = fmt.Errorf("%s: %v", what, r)
		}
	}()
	return fn()
}

// SetMitm 在 Start 前调用，启用/禁用 CONNECT MITM 模式。
// MITM 让不改协议的客户端（libmpv/WebView/系统播放器）也能获得完整 ECH：
// 代理下游用自签证书终止客户端 TLS，上游用 ECH 直连。
// 客户端需信任代理 CA 或跳过证书校验（如 mpv tls-verify=no）。
func SetMitm(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	mitmEnabled = enabled
}

// GetMitm 返回当前 MITM 模式开关状态
func GetMitm() bool {
	mu.Lock()
	defer mu.Unlock()
	return mitmEnabled
}

// GetCAPem 返回 MITM CA 证书（PEM 格式），供 Android 写入文件/安装信任
func GetCAPem() string {
	var pem string
	_ = safe("GetCAPem", func() error {
		mu.Lock()
		defer mu.Unlock()
		if server != nil {
			pem = string(server.GetMitmCAPem())
		}
		return nil
	})
	return pem
}

// Start launches a loopback-only HTTP CONNECT proxy.
func Start(listen, doh, cachePath string, noDowngrade bool) error {
	return safe("Start", func() error {
		mu.Lock()
		defer mu.Unlock()

		if server != nil {
			return nil
		}
		if listen == "" {
			return fmt.Errorf("listen address is required")
		}
		if doh == "" {
			return fmt.Errorf("DoH endpoint is required")
		}

		cfg := config.Default()
		cfg.Listen = listen
		cfg.DoH = doh
		cfg.Mode = "http"
		cfg.DNS.CachePath = cachePath
		cfg.ECH.NoDowngrade = noDowngrade
		cfg.MITM.Enabled = mitmEnabled
		if err := cfg.Validate(); err != nil {
			return err
		}

		logs = &boundedLog{}
		log.SetOutput(io.MultiWriter(os.Stderr, logs))
		s := proxy.New(cfg)
		server = s
		lastInfo = "starting on " + listen
		go func() {
			defer func() {
				if r := recover(); r != nil {
					msg := fmt.Sprintf("serve panic: %v\n%s", r, debug.Stack())
					mu.Lock()
					lastInfo = msg
					mu.Unlock()
					log.Print(msg)
				}
			}()
			err := s.ListenAndServe()
			mu.Lock()
			defer mu.Unlock()
			if server == s {
				server = nil
			}
			if err != nil {
				lastInfo = "stopped: " + err.Error()
			} else {
				lastInfo = "stopped"
			}
		}()
		lastInfo = "listening on " + listen
		// 2026-08-15 CF IP 优选：启动后后台扫最快边缘 IP（拉白嫖优选列表 +
		// TLS 握手测速），完成后前置到候选最前。不阻塞启动 —— 首个请求
		// 用现有候选（远程配置/DoH 端点 IP），扫描完成自动切换最快 IP。
		// 移动宽带上避免串行试不可达 IP 白等（CO3 实测每次卡 40s+）。
		go func() {
			start := time.Now()
			ips := cloudflare.ScanPreferredIPs(5, 8*time.Second)
			if len(ips) == 0 {
				log.Printf("[echproxy] preferred IP scan: no reachable IP (took %v)", time.Since(start))
				return
			}
			s.SetPreferredIPs(ips)
			log.Printf("[echproxy] preferred IP scan: %d fastest IPs %v (took %v)",
				len(ips), ips, time.Since(start))
		}()
		return nil
	})
}

// Stop gracefully shuts down the current proxy. It is safe to call repeatedly.
func Stop() error {
	return safe("Stop", func() error {
		mu.Lock()
		s := server
		server = nil
		mu.Unlock()
		if s == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := s.Shutdown(ctx)
		mu.Lock()
		lastInfo = "stopped"
		mu.Unlock()
		return err
	})
}

// SetEndpoints hot-updates the DoH endpoints and custom edge IPs from the
// seed TXT config without restarting the proxy.
func SetEndpoints(doh, ip string) error {
	return safe("SetEndpoints", func() error {
		mu.Lock()
		defer mu.Unlock()
		if server == nil {
			return fmt.Errorf("proxy not started")
		}
		server.SetEndpoints(doh, ip)
		return nil
	})
}

// SetOverrides hot-updates per-host fixed IP lists from the seed TXT
// `override=` field, e.g. "www.getchu.com=210.155.150.166". Hosts listed
// bypass DoH A/AAAA resolution and are dialed with plain TLS only —
// used when a specific edge IP is blocked on some carriers.
func SetOverrides(spec string) error {
	return safe("SetOverrides", func() error {
		mu.Lock()
		defer mu.Unlock()
		if server == nil {
			return fmt.Errorf("proxy not started")
		}
		server.SetOverrides(spec)
		return nil
	})
}

// IsRunning reports whether this process currently owns a started proxy.
func IsRunning() bool {
	running := false
	_ = safe("IsRunning", func() error {
		mu.Lock()
		defer mu.Unlock()
		running = server != nil
		return nil
	})
	return running
}

// LastStatus returns the concise lifecycle status. Use Diagnostics for the
// bounded protocol log.
func LastStatus() string {
	var info string
	_ = safe("LastStatus", func() error {
		mu.Lock()
		defer mu.Unlock()
		info = lastInfo
		return nil
	})
	return info
}

// Diagnostics returns lifecycle status plus the bounded Go proxy log.
func Diagnostics() string {
	var info string
	_ = safe("Diagnostics", func() error {
		mu.Lock()
		defer mu.Unlock()
		info = lastInfo
		return nil
	})
	return info + "\n--- go proxy log ---\n" + logs.String()
}
