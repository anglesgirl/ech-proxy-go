// ProbeRunner — x.com 强制 ECH 测试的流式版本
//
// 与 XProbe（一次性阻塞返回）不同，本文件提供后台启动 + 增量日志轮询，
// 让 Android UI 能实时滚动显示 DoH/ECH/HTTP 每一步进展，而不是干等。
//
//	StartProbe(doh, hosts)  → 后台 goroutine 执行测试，日志实时写入 logs
//	PollLogs()              → 返回自上次调用以来的新增日志（增量）
//	IsProbeDone()           → 测试是否结束
//	LastProbeResult()       → 最终完整报告（供导出）
package echproxy

import (
	"io"
	"log"
	"os"
	"sync"
)

var (
	probeMu      sync.Mutex
	probeRunning bool
	probeDone    bool
	probeResult  string
	// logCursor 是 PollLogs 已消费的字节位置（logs buffer 内）
	logCursor int
)

// StartProbe 在后台启动 x.com 强制 ECH 测试。日志实时写入内部 buffer，
// Android 侧通过 PollLogs 增量拉取。重复调用会忽略。
func StartProbe(doh, hosts string) error {
	return safe("StartProbe", func() error {
		probeMu.Lock()
		defer probeMu.Unlock()
		if probeRunning {
			return nil
		}
		probeRunning = true
		probeDone = false
		probeResult = ""
		logCursor = 0

		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[xprobe] panic: %v", r)
				}
				probeMu.Lock()
				probeRunning = false
				probeDone = true
				probeMu.Unlock()
			}()
			out := xprobeRunStreaming(doh, hosts)
			probeMu.Lock()
			probeResult = out
			probeMu.Unlock()
		}()
		// 确保 Go 日志接入内部 buffer（Start 未调用时也要能轮询到日志）
		log.SetOutput(io.MultiWriter(os.Stderr, logs))
		return nil
	})
}

// PollLogs 返回自上次调用以来新增的日志文本（增量轮询）。
func PollLogs() string {
	probeMu.Lock()
	defer probeMu.Unlock()
	cur := logs.String()
	// buffer 被 boundedLog 裁剪过（超 64KB 时），指针失效则从头给
	if logCursor > len(cur) {
		logCursor = 0
	}
	delta := cur[logCursor:]
	logCursor = len(cur)
	return delta
}

// IsProbeDone 报告后台测试是否已结束。
func IsProbeDone() bool {
	probeMu.Lock()
	defer probeMu.Unlock()
	return probeDone
}

// LastProbeResult 返回最终完整报告（测试结束后调用，供导出/分享）。
func LastProbeResult() string {
	probeMu.Lock()
	defer probeMu.Unlock()
	return probeResult
}
