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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
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
		// 2026-08-15 CF IP 三阶段优选（用户方案，与 CO3 同源）：
		// 有缓存(12h)立即返回；无缓存 → 采样50不同网段 + TCP延迟排序2s
		// top10 → speed.cloudflare.com 下载测速8s top3 → 写缓存。
		// 同步执行：启动即绑定最快 IP 不再乱跳，总耗时 ≤10s。
		fastStart := time.Now()
		fastIPs := cloudflare.OptimizeFastIPs(cfg.DNS.CachePath)
		if len(fastIPs) > 0 {
			s.SetPreferredIPs(fastIPs)
			log.Printf("[echproxy] preferred IP scan done in %v: %v", time.Since(fastStart), fastIPs)
		} else {
			log.Printf("[echproxy] preferred IP scan: none (took %v)", time.Since(fastStart))
		}
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

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// UploadToR2 uploads bounded diagnostic text to an S3-compatible R2 bucket.
// The caller must provide a least-privilege key scoped to the diagnostics bucket.
func UploadToR2(endpoint, bucket, objectKey, accessKey, secretKey, contentType, content string) bool {
	ok := false
	_ = safe("UploadToR2", func() error {
		t := time.Now().UTC()
		amzDate := t.Format("20060102T150405Z")
		dateStamp := t.Format("20060102")
		host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
		body := []byte(content)
		hash := sha256.Sum256(body)
		payloadHash := hex.EncodeToString(hash[:])
		canonicalURI := "/" + bucket + "/" + objectKey
		canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", contentType, host, payloadHash, amzDate)
		signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
		canonicalRequest := strings.Join([]string{"PUT", canonicalURI, "", canonicalHeaders, signedHeaders, payloadHash}, "\n")
		scope := fmt.Sprintf("%s/auto/s3/aws4_request", dateStamp)
		stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest))}, "\n")
		h := func(key, value []byte) []byte { m := hmac.New(sha256.New, key); _, _ = m.Write(value); return m.Sum(nil) }
		kDate := h([]byte("AWS4"+secretKey), []byte(dateStamp))
		kRegion := h(kDate, []byte("auto")); kService := h(kRegion, []byte("s3")); kSigning := h(kService, []byte("aws4_request"))
		signature := hex.EncodeToString(h(kSigning, []byte(stringToSign)))
		req, err := http.NewRequest(http.MethodPut, endpoint+canonicalURI, bytes.NewReader(body)); if err != nil { return err }
		req.Header.Set("Content-Type", contentType); req.Header.Set("X-Amz-Date", amzDate); req.Header.Set("X-Amz-Content-Sha256", payloadHash)
		req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", accessKey, scope, signedHeaders, signature))
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req); if err != nil { return err }; defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 { return fmt.Errorf("R2 HTTP %d", resp.StatusCode) }
		ok = true; return nil
	})
	return ok
}

// Diagnostics returns lifecycle status plus the bounded Go proxy log.
func Diagnostics() string {
	var info string
	_ = safe("Diagnostics", func() error { mu.Lock(); defer mu.Unlock(); info = lastInfo; return nil })
	return info + "\\n--- go proxy log ---\\n" + logs.String()
}
