# ech-proxy-go

通用 ECH (Encrypted Client Hello) 前置代理，用 Go 实现。

## 工作原理

1. 监听本地端口，接受 HTTP CONNECT / SOCKS5 代理请求
2. 通过 DoH 查询目标域名的 A/AAAA + HTTPS(type 65) 记录
3. 从 HTTPS 记录中提取 ECHConfig（支持文本格式和 RFC 3597 线格式）
4. 用 Go 1.23+ `crypto/tls` 原生 ECH 支持建立 TLS 连接
5. 双向转发数据

## 特性

- **HTTP CONNECT 代理** — 标准 HTTP 代理协议，任何 HTTP 客户端可用
- **SOCKS5 代理** — 同时支持 SOCKS5 协议
- **多 DoH 端点** — 逗号分隔多个 DoH URL，按顺序尝试，适合 GFW 环境
- **DNS 缓存** — 带 TTL 的内存缓存 + 过期条目兜底
- **ECH 配置文件缓存** — 12 小时 TTL 文件缓存，DoH 不可用时自动回退
- **ECH 拒绝重试** — 服务器拒绝 ECH 时自动使用 `retry_configs` 重试
- **禁止降级模式** — `no_downgrade` 选项防止 ECH 主机回退到明文 TLS（保护 SNI）
- **AS13335 IP 校验** — 自定义边缘 IP 只接受 Cloudflare AS13335 地址
- **Android 证书池** — 自动加载 Android 系统 CA 证书（DER + PEM）
- **配置文件** — YAML 配置文件，灵活可配
- **优雅关闭** — 支持 SIGINT/SIGTERM 信号优雅退出
- **跨平台** — 纯 Go 静态编译，支持 Linux/Android/macOS/Windows

## 快速开始

### 编译

```bash
go build -o ech-proxy ./cmd/ech-proxy
```

### 运行

```bash
# 使用默认配置
./ech-proxy

# 指定端口和 DoH
./ech-proxy 17171 https://1.1.1.1/dns-query

# 使用配置文件
./ech-proxy -config config.yaml
```

### 作为 Android 应用前置代理

```bash
# 交叉编译 (静态二进制，Android 可直接运行)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o ech-proxy-arm64 ./cmd/ech-proxy
CGO_ENABLED=0 GOOS=linux GOARCH=arm   go build -ldflags="-s -w" -o ech-proxy-arm ./cmd/ech-proxy
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ech-proxy-amd64 ./cmd/ech-proxy
```

将二进制放入 Android 应用的 assets 目录，运行时提取到私有目录执行，设置 HTTP 代理指向 `127.0.0.1:17171`。

### 客户端配置

```bash
# curl
curl -x http://127.0.0.1:17171 https://example.com

# 环境变量
export http_proxy=http://127.0.0.1:17171
export https_proxy=http://127.0.0.1:17171

# OkHttp (Android)
val proxy = Proxy(Proxy.Type.HTTP, InetSocketAddress("127.0.0.1", 17171))
val client = OkHttpClient.Builder().proxy(proxy).build()
```

## 配置文件

```yaml
listen: "127.0.0.1:17171"
# 支持多个 DoH 端点，逗号分隔
doh: "https://1.1.1.1/dns-query,https://cloudflare-dns.com/dns-query"
mode: "http"           # http / socks5 / both
tls:
  timeout: "15s"
  fallback_plain: true  # ECH 失败回退普通 TLS
dns:
  cache_ttl: "300s"
  prefer_ipv4: true
  cache_path: "/tmp/ech_config.json"  # ECH 配置文件缓存（可选）
ech:
  custom_ips: "104.20.8.2,104.20.9.2"  # 自定义 Cloudflare 边缘 IP
  no_downgrade: false                    # 禁止降级到明文 TLS
```

### 环境变量覆盖

| 变量 | 说明 |
|------|------|
| `DOH_URL` | 覆盖 DoH 地址 |
| `LISTEN_ADDR` | 覆盖监听地址 |
| `ECH_CUSTOM_IPS` | 覆盖自定义边缘 IP |

## 项目结构

```
ech-proxy-go/
├── cmd/ech-proxy/          # 主入口
├── internal/
│   ├── certutil/           # Android 证书池加载 (DER + PEM)
│   ├── cloudflare/         # AS13335 IP 范围校验
│   ├── config/             # 配置加载和校验
│   ├── dns/                # DoH 查询 + DNS 缓存 + ECH 文件缓存
│   ├── proxy/              # HTTP CONNECT + SOCKS5 代理
│   └── tlsconn/            # ECH TLS 连接 (retry_configs + 多候选)
├── configs/                # 示例配置
└── .github/workflows/      # CI 自动编译
```

## 依赖

- Go 1.23+ (需要 `crypto/tls` ECH 支持)
- gopkg.in/yaml.v3 (配置文件解析)

## 许可证

MIT
