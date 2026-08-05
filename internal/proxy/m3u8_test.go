package proxy

import (
	"strings"
	"testing"
)

func TestRewriteM3U8Relative(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXTINF:10.0,
seg-1.ts
#EXTINF:10.0,
sub/seg-2.ts
#EXT-X-ENDLIST
`
	out := string(rewriteM3U8([]byte(body), "javchu.com", "/hls/video.m3u8"))
	if !strings.Contains(out, "https://javchu.com/hls/seg-1.ts") {
		t.Fatalf("relative seg not rewritten: %s", out)
	}
	if !strings.Contains(out, "https://javchu.com/hls/sub/seg-2.ts") {
		t.Fatalf("nested relative seg not rewritten: %s", out)
	}
	if !strings.Contains(out, "#EXT-X-VERSION:3") {
		t.Fatalf("directive lost: %s", out)
	}
	t.Logf("OK:\n%s", out)
}

func TestRewriteM3U8AbsoluteAndPreserve(t *testing.T) {
	body := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1280000
/videos/720p.m3u8
https://cdn.javchu.com/1080p.m3u8
#EXT-X-ENDLIST
`
	out := string(rewriteM3U8([]byte(body), "javchu.com", "/watch/123"))
	if !strings.Contains(out, "https://javchu.com/videos/720p.m3u8") {
		t.Fatalf("absolute path not rewritten: %s", out)
	}
	if !strings.Contains(out, "https://cdn.javchu.com/1080p.m3u8") {
		t.Fatalf("already-absolute URL must be preserved: %s", out)
	}
	t.Logf("OK:\n%s", out)
}

func TestRewriteM3U8NonPlaylist(t *testing.T) {
	body := []byte("this is not a playlist")
	if out := rewriteM3U8(body, "javchu.com", "/a.ts"); string(out) != string(body) {
		t.Fatalf("non-playlist must pass through unchanged")
	}
}

func TestRewriteM3U8ParentDir(t *testing.T) {
	body := `#EXTM3U
#EXTINF:10.0,
../common/seg-3.ts
`
	out := string(rewriteM3U8([]byte(body), "javchu.com", "/hls/1080p/video.m3u8"))
	// 播放列表在 /hls/1080p/ 下，../common → /hls/common
	if !strings.Contains(out, "https://javchu.com/hls/common/seg-3.ts") {
		t.Fatalf("parent-dir not resolved: %s", out)
	}
	t.Logf("OK:\n%s", out)
}
