# Gomobile API

The `mobile/echproxy` package is the Android-facing entry point. Build it as an
AAR with `gomobile bind`; do not expose or copy `internal/` implementation into
the app.

## Exported API

```text
Start(listen, doh, cachePath string, noDowngrade bool) error
Stop() error
IsRunning() bool
LastStatus() string
```

- `listen`: loopback endpoint chosen by the app, e.g. `127.0.0.1:34043`.
- `doh`: one or more comma-separated RFC 8484 DoH endpoints.
- `cachePath`: app-private path for public ECH configuration cache; use an
  empty string only for temporary/testing integrations.
- `noDowngrade`: set `true` only when the product must fail rather than expose
  SNI through a plain-TLS fallback.

The Android app starts this proxy asynchronously, then supplies the port to
its normal `ProxySelector` or individual clients. If `Start` fails, retain the
application's direct/network-user-proxy behavior.

## Build

```bash
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init
gomobile bind -target=android/arm64 -androidapi 29 \
  -o app/libs/echproxy.aar ./mobile/echproxy
```

The resulting library contains `libgojni.so`. A consuming APK must be checked
for `lib/arm64-v8a/libgojni.so` after its Gradle build.
