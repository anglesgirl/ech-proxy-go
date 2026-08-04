# ECH Proxy Integration Rules for AI Agents

This repository is the **canonical reusable ECH proxy**.  When adding ECH to an
Android application or another client, extend this project or consume its
library/binary; do **not** reimplement DNS wire parsing, ECH configuration
lookup, TLS retry behavior, or local-proxy plumbing inside the host app.

## First read

Before changing proxy or integration behavior, read in this order:

1. `README.md` — public proxy contract and configuration.
2. `docs/ARCHITECTURE.md` — invariants and established design decisions.
3. `docs/ANDROID_INTEGRATION.md` — required Android wiring and verification.
4. The relevant package under `internal/`.

## Non-negotiable invariants

- Resolve protected destinations through configured DoH; never silently use
  system DNS as a fallback for an ECH path.
- Use RFC 8484 `application/dns-message` POST for DoH. Do not regress to the
  JSON GET API: `dns.alidns.com/dns-query` accepts DNS wire format, while its
  JSON API is `/resolve`.
- ECH must be **opportunistic with explicit observability**: attempt ECH only
  when an ECHConfigList is available; allow plain-TLS fallback only when the
  configuration permits it; log whether `ECHAccepted` was true.
- Preserve client connectivity if the local proxy cannot start. The host app
  must use a direct-connection fallback rather than making all networking fail.
- Keep the proxy loopback-only (`127.0.0.1`); do not expose it on a LAN address
  by default.
- Cache public DNS/ECH metadata only. Never persist HTTP bodies, cookies,
  authorization headers, user credentials, or TLS private keys.
- Do not route local addresses, `localhost`, `127.0.0.1`, `.local`, or the DoH
  bootstrap/config request back through the proxy, or proxy recursion results.

## Android implementation boundary

- Go owns: DoH resolution, HTTPS/SVCB parsing, ECH config selection/cache,
  TLS handshake/retry/fallback, and HTTP CONNECT/SOCKS relay.
- Android owns: lifecycle, choosing a free loopback port, user-facing settings,
  network-stack attachment, network-security XML, remote configuration policy,
  and diagnostic-log export.
- Prefer a normal HTTP proxy (`OkHttp Proxy` / a `ProxySelector`) so clients
  issue `CONNECT` themselves. Avoid rewriting every HTTPS URL into a local HTTP
  URL unless the app cannot use a standard proxy.
- If URL rewriting is unavoidable, retain original-host cookie semantics:
  inject cookies for the original host and store `Set-Cookie` back under that
  original host. Otherwise login sessions break.

## Change discipline

1. Reuse existing packages; do not copy their logic into an app module.
2. Keep cold start non-blocking: start from a valid cached/local configuration,
   then refresh remote configuration in the background and hot-apply it.
3. Keep network-stack coverage explicit: ordinary OkHttp, image loader,
   WebView, media player, and download/update client may each require separate
   wiring.
4. Add a regression test for DNS/TLS parsing changes. At minimum run `go test
   ./...`, `go vet ./...`, and a local proxy smoke test before committing.
5. Never claim ECH works based only on HTTP 200. Verify a log/status signal
   containing `ECHAccepted=true` for an actual protected host.

## Delivery checklist

A change is complete only when:

- all changed network stacks are enumerated in the PR/commit message;
- fallback behavior is tested with the proxy unavailable;
- Android cleartext-to-loopback policy is present when using an HTTP loopback
  proxy on Android 9+;
- no secrets or personal traffic are added to source, examples, or logs;
- build/tests and the ECH acceptance check have real output.
