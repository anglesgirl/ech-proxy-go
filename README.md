# ech-proxy-go

通用 **ECH（Encrypted Client Hello）前置代理**，以 Go 实现 HTTP CONNECT 与 SOCKS5。它是客户端项目的共享 ECH 传输层：应用接入本仓库，而不是各自重新实现 DoH、HTTPS/SVCB、ECH TLS 与回退逻辑。

> **集成新应用或让 AI 改 ECH 前，先读：**
> - [`AGENTS.md`](AGENTS.md)：AI/贡献者硬性规则
> - [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)：架构、边界与性能约束
> - [`docs/ANDROID_INTEGRATION.md`](docs/ANDROID_INTEGRATION.md)：Android 实战集成清单

## 工作原理

```text
客户端（OkHttp / WebView / 播放器 / 下载器）
  -> 本机 127.0.0.1 HTTP CONNECT 或 SOCKS5
  -> DoH 查询 A / AAAA / HTTPS (SVCB, type 65)
  -> 有 ECHConfigList 时用 ECH TLS；否则普通 TLS 或受策略控制的降级
  -> 目标 HTTPS 服务
```

本机代理不解密 HTTPS 应用数据、也不是中间人；它只负责 DoH 解析及到上游的 TLS 建连。

## 特性

- **HTTP CONNECT 与 SOCKS5**：可供标准 HTTP 客户端使用。
- **多 DoH 端点**：逗号分隔、顺序尝试，适配受限网络。
- **RFC 8484 二进制 DoH**：`application/dns-message` POST；兼容 AliDNS `/dns-query`。
- **A / AAAA / HTTPS / TXT**：解析 HTTPS/SVCB 的 `ech=` 及 RFC 3597 wire 格式。
- **缓存**：内存 DNS 缓存、公共 ECHConfigList 文件缓存、失效内存缓存兜底。
- **ECH 重试**：服务器提供 `retry_configs` 时重试并缓存。
- **可控降级**：默认可普通 TLS 兜底保证连通；`no_downgrade` 可禁止泄露 SNI 的降级。
- **候选 IP 与 AS13335 过滤**：支持经校验的 Cloudflare 边缘 IP。
- **跨平台证书**：Android 扫描系统证书目录；桌面系统使用原生证书库。
- **可观测性**：日志记录 DoH、ECH 配置来源及 `ECHAccepted=true/false`。

## 快速开始

### 编译与运行

```bash
go build -o ech-proxy ./cmd/ech-proxy
./ech-proxy

# 指定端口与 DoH（多个端点以逗号分隔）
./ech-proxy 17171 https://1.1.1.1/dns-query,https://cloudflare-dns.com/dns-query

# 使用配置文件
./ech-proxy -config config.yaml
```

### 使用客户端代理

```bash
curl -x http://127.0.0.1:17171 https://example.com

export http_proxy=http://127.0.0.1:17171
export https_proxy=http://127.0.0.1:17171
```

OkHttp：

```kotlin
val proxy = Proxy(Proxy.Type.HTTP, InetSocketAddress("127.0.0.1", 17171))
val client = OkHttpClient.Builder().proxy(proxy).build()
```

Android 应用的完整生命周期、WebView/Coil/播放器覆盖、Cookie 与网络安全配置要求，见 [`docs/ANDROID_INTEGRATION.md`](docs/ANDROID_INTEGRATION.md)。

## 配置文件

```yaml
listen: "127.0.0.1:17171"
doh: "https://1.1.1.1/dns-query,https://cloudflare-dns.com/dns-query"
mode: "http"           # http / socks5 / both

tls:
  timeout: "15s"
  fallback_plain: true   # ECH 最终失败时是否允许普通 TLS

dns:
  cache_ttl: "300s"
  prefer_ipv4: true
  cache_path: "ech_config.json" # 公共 ECHConfigList 缓存，可选

ech:
  custom_ips: ""        # 可选；仅接受经 AS13335 验证的 IP
  no_downgrade: false    # true 时禁止 ECH 主机降级到普通 TLS
```

`ech.no_downgrade: true` 适合必须避免 SNI 泄露的部署，但 ECH 配置失效时会牺牲连通性；这是产品策略选择，不应被代码意外改变。

## DoH 兼容性提示

`https://dns.alidns.com/dns-query` 只接受 RFC 8484 **二进制 POST**；如果使用 JSON 查询，AliDNS 的 JSON 端点是：

```text
https://dns.alidns.com/resolve
```

不要把 `/dns-query` 改成 JSON GET，否则会造成 DoH/远程 TXT 配置失败。

## 验证

```bash
go test ./...
go vet ./...
```

HTTP 200 仅证明请求可达，**不能证明 ECH 已协商成功**。必须在真实目标请求日志中确认：

```text
ECHAccepted=true
```

Android 集成还必须验证：冷启动不等待远程配置、主 OkHttp/图片/WebView/播放/更新客户端均已接线，以及代理不可用时仍能直连。

## 项目结构

```text
ech-proxy-go/
├── AGENTS.md                    # AI 和贡献者不可违反的集成规则
├── cmd/ech-proxy/               # CLI 入口
├── internal/
│   ├── certutil/                # 跨平台证书加载
│   ├── cloudflare/              # AS13335 IP 校验
│   ├── config/                  # YAML 配置
│   ├── dns/                     # DoH、SVCB/TXT、缓存
│   ├── proxy/                   # HTTP CONNECT + SOCKS5 + relay
│   └── tlsconn/                 # ECH TLS、retry_configs、降级
├── android-ui/                  # Compose 设置组件（可选）
├── docs/
│   ├── ARCHITECTURE.md
│   └── ANDROID_INTEGRATION.md
└── .github/workflows/           # 多平台构建与 Release
```

## 许可证

MIT
