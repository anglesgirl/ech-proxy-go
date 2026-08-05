package proxy

import (
	"bytes"
	"net/url"
	"path"
	"strings"
)

// rewriteM3U8 converts every relative/absolute reference inside an HLS master
// or media playlist into an absolute https:// URL based on the original target
// host (the X-Ech-Target). Without this, a playlist served through the proxy
// would contain relative segment paths (e.g. "seg-1.ts") that the player
// resolves against the proxy URL (http://127.0.0.1:port/...), losing the
// upstream host and 404ing.
//
// Lines that are not references (directives starting with '#', blank lines,
// comments, already-absolute http(s) URLs) are left untouched.
func rewriteM3U8(body []byte, target string, reqPath string) []byte {
	if !isM3U8(body) {
		return body
	}
	// Base directory of the playlist on the upstream, for resolving relative
	// segment references ("seg-1.ts" -> "https://host/dir/seg-1.ts").
	baseDir := path.Dir(reqPath)
	if baseDir == "." || baseDir == "/" {
		baseDir = ""
	}

	lines := bytes.Split(body, []byte("\n"))
	for i, line := range lines {
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Already absolute http(s) URL: leave untouched. (The player will
		// request it through OkHttp/EchInterceptor, so it still goes via ECH.)
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			continue
		}
		abs := resolveReference(target, baseDir, trimmed)
		lines[i] = []byte(abs)
	}
	return bytes.Join(lines, []byte("\n"))
}

// resolveReference builds "https://<target><resolvedPath>" for a playlist
// reference. Handles absolute-path references ("/hls/seg.ts") and relative
// references ("seg.ts", "../seg.ts", "sub/seg.ts").
func resolveReference(target, baseDir, ref string) string {
	u := &url.URL{Scheme: "https", Host: target}
	if strings.HasPrefix(ref, "/") {
		u.Path = ref
	} else {
		u.Path = path.Join("/", baseDir, ref)
	}
	// path.Join cleans ".." segments; ensure query/fragment are preserved.
	if idx := strings.IndexAny(ref, "?#"); idx >= 0 {
		rest := ref[idx:]
		if strings.HasPrefix(rest, "?") {
			u.RawQuery = rest[1:]
		} else if strings.HasPrefix(rest, "#") {
			u.Fragment = rest[1:]
		}
	}
	return u.String()
}

// isM3U8 reports whether the body looks like an HLS playlist, regardless of
// the Content-Type header (some CDNs serve it as text/plain).
func isM3U8(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("#EXTM3U"))
}
