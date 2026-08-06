package proxy

import "testing"

func TestResolveTargetHeaderOnly(t *testing.T) {
	// ExoPlayer/MPV 初始 m3u8 请求：header 模式，路径不带前缀
	target, path, ok := resolveTarget("t33.cdn2020.com", "/video/m3u8/2026/01/28/abc/index.m3u8")
	if !ok || target != "t33.cdn2020.com" || path != "/video/m3u8/2026/01/28/abc/index.m3u8" {
		t.Fatalf("header-only: got target=%q path=%q ok=%v", target, path, ok)
	}
}

func TestResolveTargetHeaderPlusPrefix(t *testing.T) {
	// MPV 分片请求：http-header-fields 全局带 header + path-prefix URL → 剥前缀
	target, path, ok := resolveTarget("t33.cdn2020.com", "/t33.cdn2020.com/video/m3u8/2026/01/28/abc/0000.ts")
	if !ok || target != "t33.cdn2020.com" || path != "/video/m3u8/2026/01/28/abc/0000.ts" {
		t.Fatalf("header+prefix: got target=%q path=%q ok=%v", target, path, ok)
	}
}

func TestResolveTargetPrefixOnly(t *testing.T) {
	// ExoPlayer 分片请求（无 header）：path-prefix 模式，从路径解析
	target, path, ok := resolveTarget("", "/t33.cdn2020.com/video/m3u8/2026/01/28/abc/0000.ts")
	if !ok || target != "t33.cdn2020.com" || path != "/video/m3u8/2026/01/28/abc/0000.ts" {
		t.Fatalf("prefix-only: got target=%q path=%q ok=%v", target, path, ok)
	}
}

func TestResolveTargetNoHeaderNoPrefix(t *testing.T) {
	// 无 header 且路径第一段不是 host（无点号）→ 拒绝
	_, _, ok := resolveTarget("", "/watch/123")
	if ok {
		t.Fatal("no-header plain path must be rejected")
	}
	_, _, ok = resolveTarget("", "/video/m3u8/abc/index.m3u8")
	if ok {
		t.Fatal("no-header plain path must be rejected")
	}
}

func TestResolveTargetEmpty(t *testing.T) {
	_, _, ok := resolveTarget("", "")
	if ok {
		t.Fatal("empty target+path must be rejected")
	}
	_, _, ok = resolveTarget(" ", "/path")
	if ok {
		t.Fatal("blank header with plain path must be rejected")
	}
}
