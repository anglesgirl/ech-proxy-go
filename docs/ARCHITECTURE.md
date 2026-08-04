# ECH Proxy Architecture

## Purpose

`ech-proxy-go` is the shared ECH transport layer for clients that need to reach
HTTPS hosts in networks where visible SNI can be blocked or reset. It provides
a loopback HTTP CONNECT and SOCKS5 proxy. The client retains normal HTTPS
semantics; the proxy resolves the origin via DoH and establishes the upstream
TLS connection.

This repository intentionally owns the difficult protocol work. Applications
must integrate it rather than carrying private copies of ECH, DNS, or TLS code.

## Request path

```text
Client (OkHttp / WebView / player / downloader)
  -> 127.0.0.1 local HTTP CONNECT or SOCKS5 proxy
  -> DoH A + AAAA + HTTPS(SVCB, type 65)
  -> ECH TLS when ECHConfigList is available
  -> ordinary TLS when no ECHConfigList exists, or controlled fallback
  -> origin server
```

The encrypted application request remains end-to-end TLS between the client
and origin. The loopback proxy is a local transport adapter, not a TLS
man-in-the-middle.

## Ownership boundaries

| Layer | Owns | Must not duplicate |
|---|---|---|
| `internal/dns` | multi-endpoint DoH, A/AAAA/HTTPS/TXT parsing, DNS and public ECH config cache | DNS wire encoder/parser in each application |
| `internal/tlsconn` | ECH TLS candidate dialing, retry configs, ECH status logs, controlled downgrade | app-side `crypto/tls` ECH handshake code |
| `internal/proxy` | HTTP CONNECT, SOCKS5, relay and timeouts | bespoke local proxy servers in every app |
| Client integration | lifecycle, proxy attachment, UI/config policy, logs | protocol internals |

## Connection decision tree

```text
Resolve origin through configured DoH
  |
  +-- cached DNS result valid? -> reuse it
  |
  +-- otherwise query A, AAAA and HTTPS in parallel

ECHConfig available?
  |
  +-- yes -> try resolved candidate IPs with ECH
  |           |
  |           +-- server returns retry_configs -> retry once and persist it
  |           +-- succeeds -> log ECHAccepted=true
  |           +-- all attempts fail -> plain TLS only if fallback is allowed
  |
  +-- no -> ordinary TLS over DoH-resolved address
```

A cache hit avoids repeated DoH work at app startup. Cache entries are public
routing metadata only. A stale in-memory DNS entry may keep a user connected
when DoH is briefly unavailable; it is not a license to use poisoned system
DNS.

## ECH configuration sources

For a candidate Cloudflare host, use the first available source:

1. Valid local public ECHConfigList cache (short TTL).
2. `cloudflare-ech.com` HTTPS record.
3. Target host HTTPS record (`ech=` / SVCB param key 5).
4. Server-provided TLS `retry_configs` after an ECH rejection.

The existing implementation has most of this pipeline. Future work must keep
source order and cache writes explicit; do not add a hard-coded, permanent ECH
public key.

## DoH compatibility rule

Use RFC 8484 binary POST:

```http
POST /dns-query HTTP/1.1
Content-Type: application/dns-message
Accept: application/dns-message
```

Do **not** switch the resolver to DNS JSON GET as a general replacement.
`dns.alidns.com/dns-query` requires RFC 8484 binary traffic; its distinct JSON
endpoint is `/resolve`. The binary parser must handle:

- A (1), AAAA (28), TXT (16), and HTTPS/SVCB (65);
- compressed DNS names;
- multi-chunk TXT RDATA (join chunks before parsing config);
- raw RFC 3597 representation of HTTPS records and SVCB parameter key 5.

## Security and fallback policy

`tls.fallback_plain` keeps general applications usable when a host has stale or
broken ECH metadata. This necessarily exposes ordinary SNI for that request.
For a censorship-resistant deployment, set `ech.no_downgrade: true`; it
overrides plain fallback. The right choice is product policy, not an accidental
implementation detail.

Custom edge IPs must be validated as Cloudflare AS13335 before use. They are
routing candidates, not a reason to bypass certificate validation or change
the real TLS server name.

## Performance rules established in production

1. **No network wait on app startup.** Start the local proxy with a cached or
   local configuration; fetch operator settings after startup.
2. **Cache DNS and public ECH configs.** Re-query only after TTL expiry or a
   failed/rejected ECH attempt that requires refresh.
3. **Reuse HTTP/TLS client infrastructure.** Do not make every image request
   create a new client/transport.
4. **Do not blindly attempt Cloudflare ECH for unrelated origins.** It creates
   a failed handshake plus downgrade penalty. Apply Cloudflare-specific config
   only when the host/routing policy supports it; otherwise use the target's own
   HTTPS record or ordinary TLS.
5. **Measure each route.** Log origin, elapsed milliseconds, HTTP result, and
   ECH acceptance state. HTTP 200 alone proves reachability, not ECH.

## Status vocabulary

A client diagnostic surface should distinguish:

- `proxy listening` — loopback server started;
- `DoH cache hit` / `DoH resolved` — resolution source;
- `ECHAccepted=true` — ECH negotiated successfully;
- `retry_configs` — server supplied an ECH configuration refresh;
- `plain TLS fallback` — connected but ECH protection was not retained;
- `proxy unavailable -> direct fallback` — client still works without local
  proxy.

Never log cookies, authorization headers, full request bodies, or raw user
identifiers.
