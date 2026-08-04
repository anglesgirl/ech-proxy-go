# Android Integration Guide

This guide records the production integration pattern validated in
Han1meViewer. It is a checklist for integrating `ech-proxy-go` into another
Android app without recreating its protocol layer.

## Choose the delivery form

| Need | Recommended form |
|---|---|
| App bundles proxy code | Build a `gomobile` AAR and call its small lifecycle API from Kotlin. |
| App ships a native executable | Package an ABI-specific binary, extract to `filesDir`, execute it, and supervise it. |
| Desktop/service consumer | Use `cmd/ech-proxy` directly with YAML configuration. |

For Android, prefer an AAR when possible: no executable-permission/extraction
problems and lifecycle is explicit. The Go API exported through gomobile must
stay small and primitive-only (strings, booleans, errors/status strings).

## Required lifecycle

1. Pick an ephemeral loopback port using `ServerSocket(0)`.
2. Start the Go proxy on `127.0.0.1:<port>` from an IO coroutine.
3. Publish the port only after startup succeeds.
4. Start with cached endpoint settings or a local DoH default. **Do not delay
   Application startup for a TXT/remote configuration request.**
5. Refresh remote config in the background. Persist the last valid DoH list
   and optional edge-IP list; hot-apply changed endpoints if the Go API
   supports it, otherwise restart only the proxy.
6. On proxy failure or shutdown, clear the published port and make every client
   use its normal direct path.
7. Poll/collect the Go status into the app's bounded diagnostic log. Export the
   log as a file for user reports rather than asking for a large logcat paste.

A minimal manager has this contract:

```kotlin
object EchProxyManager {
    @Volatile var port: Int = -1
        private set
    val isRunning get() = port > 0

    fun startAsync(context: Context)
    suspend fun stop()
    fun status(): String
}
```

## Android network security configuration

An Android 9+ app normally rejects cleartext HTTP, including an HTTP request to
a loopback proxy. If the integration sends HTTP to `127.0.0.1`, add a focused
network security config; do not enable cleartext globally.

`AndroidManifest.xml`:

```xml
<application
    android:networkSecurityConfig="@xml/network_security_config" ... />
```

`res/xml/network_security_config.xml`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<network-security-config>
    <domain-config cleartextTrafficPermitted="true">
        <domain includeSubdomains="false">127.0.0.1</domain>
        <domain includeSubdomains="false">localhost</domain>
    </domain-config>
</network-security-config>
```

The upstream connection from Go remains HTTPS. This exception is only for the
on-device hop.

## Attach every network stack deliberately

Android applications often have several unrelated HTTP stacks. A successful
homepage test does not prove image loading, login WebViews, updates, or video
playback use ECH.

| Stack | Integration approach | Required test |
|---|---|---|
| Main OkHttp/API client | Configure standard HTTP proxy or add an interceptor/client factory that routes to loopback. | Protected API/page request shows `ECHAccepted=true`. |
| Coil / image loader | Pass the same proxied `OkHttpClient` or `Call.Factory` to the image loader. | Scroll image-heavy list; verify routes and jank. |
| WebView | Use supported `ProxyController` where suitable, or intercept requests through a dedicated proxied OkHttp client. | Login HTML, CSS, JS, favicon all render; cookies persist. |
| ExoPlayer / media | Configure its HTTP data source/client or proxy property. | Real protected video plays and seeks. |
| MPV | Configure `http-proxy=http://127.0.0.1:<port>` (and HTTPS equivalent when needed). | MPV playback works. |
| Downloads and update checks | Give their dedicated clients the proxy/adapter too. | Download/update request completes and records route. |

Keep an inventory in the host repository. New clients are unprotected until
explicitly attached.

## Preferred connection model

Use a real HTTP proxy whenever the client supports it:

```kotlin
val proxy = Proxy(Proxy.Type.HTTP, InetSocketAddress("127.0.0.1", port))
val client = OkHttpClient.Builder().proxy(proxy).build()
```

That preserves HTTPS origin URLs, cookies, redirects, and CONNECT semantics.

### URL rewrite fallback

Some application paths cannot consume a `Proxy`. A fallback adapter can rewrite
an HTTPS request to `http://127.0.0.1:<port>/<original path>` and add a target
header, for example `X-Ech-Target: original.example`. The proxy must reconstruct
an HTTPS request upstream.

This is more invasive. If its URL becomes `127.0.0.1`, cookie jars will no
longer match the original host. The adapter must therefore:

1. read cookies using the original hostname and inject `Cookie`;
2. parse `Set-Cookie` from the response;
3. save cookies using the original hostname;
4. bypass all loopback/local targets to prevent recursive routing.

Do not tell WebView to display raw proxied HTTP bytes with an incorrect MIME
string. Pass only the MIME type (for example `text/html`), not a complete
`Content-Type` header value containing charset parameters, and preserve response
headers/encoding when using `WebResourceResponse`.

## Remote configuration

A TXT record may supply non-secret operating parameters:

```text
v=ech1;doh=https://one.example/dns-query;doh2=https://two.example/dns-query;ip=203.0.113.10,203.0.113.11
```

Rules:

- Fetch TXT through DoH, not system DNS.
- Query with RFC 8484 binary POST (`application/dns-message`).
- Join split TXT chunks before parsing `key=value` values.
- Use multiple DoH candidates in sequence.
- Remote config is an optimization. A failure must retain the prior cache or
  local default and never block startup.
- Treat the TXT record as public configuration; do not put credentials in it.
- Validate custom edge IPs in Go against the allowed network policy.

In Mainland China, a Cloudflare Gateway DoH endpoint can itself be unavailable.
Keep a suitable local fallback such as AliDNS. Important distinction:
`https://dns.alidns.com/dns-query` is RFC 8484 binary POST; AliDNS's JSON API is
`https://dns.alidns.com/resolve`.

## Observability and acceptance test

Collect a concise status string from Go. The only positive ECH signal is an
actual TLS state with:

```text
ECHAccepted=true
```

A release candidate must be tested on device with:

1. cold app start — UI appears without waiting for remote config;
2. protected page/API — response succeeds and status reports `ECHAccepted=true`;
3. non-ECH/CDN host — response succeeds via normal TLS without an unnecessary
   ECH failure/downgrade delay;
4. image list — images load via the intended client and scrolling remains
   acceptable;
5. WebView login — document renders normally and login cookies survive;
6. media player(s) — actual playback works;
7. proxy stopped/unavailable — app follows direct fallback rather than losing
   all networking;
8. Android 9+ device — loopback cleartext exception works, without global
   cleartext permission.

Record elapsed time per route in a bounded log so a regression can be compared
with the upstream app. Do not report a performance improvement without a
baseline and a real device measurement.

## Common failure modes

| Symptom | Likely cause | Correct fix |
|---|---|---|
| `Cleartext HTTP traffic not permitted` | Missing loopback network security config | Allow only `127.0.0.1`/`localhost` as above. |
| DoH succeeds on one provider but fails on AliDNS | JSON GET was sent to `/dns-query` | Use binary RFC 8484 POST, or JSON only at `/resolve`. |
| App stalls on launch | Remote TXT fetched synchronously | Start from cache/default and refresh asynchronously. |
| Every image takes seconds | New client/transport or failed ECH attempted for unrelated CDN | Reuse client, cache metadata, route non-ECH origins directly/ordinary TLS. |
| WebView shows source/code or blank page | Bad MIME/encoding/headers in intercepted response | Supply correct MIME type, encoding, and response headers. |
| Login state disappears | Loopback URL makes cookies belong to `127.0.0.1` | Preserve cookies under original host, or use standard proxy model. |
| App networking all fails if proxy fails | No direct fallback | Do not attach/reroute when port is invalid; restore normal clients on stop. |
| HTTP 200 but no censorship protection | Plain TLS fallback occurred | Inspect status for `ECHAccepted=true`, then adjust ECH config/policy. |
