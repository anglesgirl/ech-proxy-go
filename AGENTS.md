# AGENTS.md — ech-proxy-go 操作约束（给 AI 的提示）

> 本文件的目的是防止 AI（和人类）在改动本仓库时反复犯同样的错误。
> **任何修改本仓库代码、被它驱动的 Android 集成、或排查连接问题之前，请先读完本文件。**
> 这些不是建议，是经过生产验证的硬性约束。违反它们会导致连接失败 / 离开 / 无限重启。

---

## 0. 一句话职责

本仓库是 **ECN（Encrypted Client Hello）TCP/HTTP 代理** 的实现：DNS(DoH) 解析、ECH 握手、TLS 降级、缓存、CONNECT 隧道、应用层转发。Android App（Han1meViewer-ECH）通过 gomobile 生成的 AAR 调用它。

---

## 1. 必须遵循的约束

### 1.1 【致命】CONNECT 隧道绝不返回已握手的 TLS 连接

`internal/proxy/server.go` 的 `handleHTTP`：
- CONNECT 隧道 = **纯 TCP 转发**（`net.DialTimeout` → `Relay`）。客户端（WebView/MPV/系统代理）自己在隧道内做 TLS。
- **绝不能用 `DialECH` 返回已握手 TLS 连接给 CONNECT** —— 客户端会再握手一次 → 双重加密 → `Unable to parse TLS packet header`。

ECH 只通过**应用层路径**完成，见 1.2。

### 1.2 【核心】App 的正确接入模型：应用层 `X-Ech-Target`，不是 CONNECT

OkHttp（App 的站点请求）通过 `EchInterceptor` 把 `https://host/path` 改写成：
```
http://127.0.0.1:<port>/path   +   X-Ech-Target: host
```
Go 侧 `handleAppLayer` 自己完成 ECH/普通 TLS 连上游，**返回明文 HTTP**。客户端再也不第二次握手。

**为什么不能用 CONNECT 隧道接入 OkHttp**：CONNECT 无法隐藏 SNI，GFW 会重置 javchu.com 等封锁站点。所以 Android 侧的 `HProxySelector.select()` 在 ECH 开启时**必须返回 `Proxy.NO_PROXY`**，让改写后的请求直连本机代理，绝不进入 CONNECT 隧道。

### 1.3 【关键】DNS 解析：多种子 IP-DoH TXT，别名 DoH 列表

- **种子（bootstrap）必须用 IP 直连格式的 DoH，且必须配置多个种子**——单一种子一旦失效/被劫持，App 就没网。**禁止用单一 DoH 端点做种子**。
- 种子候选（按顺序尝试，全部失败才降级）：
  - `https://223.5.5.5/resolve`（阿里 alidns，IP 直连，JSON）
  - `https://101.226.4.6/resolve`（360，IP 直连，JSON）
  - `https://doh.pub/resolve`（腾讯 DNSPod，域名兜底）
  - （备用：`https://223.6.6.6/resolve` 阿里备用 IP）
- 从 `ech-config.anglesgirl.eu.org` 的 **TXT 记录** 拉配置（`doh=`/`doh2=`/`doh3=`/`ip=`），用返回的 DoH 端点和自定义边缘 IP 启动/热更新代理。
- **禁止用域名形式的 DoH 端点做种子主查询**——部分网络会劫持 DoH 域名的解析（劫持后返回的 TXT 全是伪造的）。IP 直连跳过 DoH 域名解析环节。
- TXT 里的 `doh=` 是 **cloudflare-gateway**（大陆可能被墙）。
- **注意：IP 直连只防端点劫持，不解决目标域名本身的污染**（alidns 对 hanime1.me 曾返回 Facebook 段假 IP `31.13.84.x`/`128.242.240.221`，导致假 `no ECHConfig ... plain TLS`）。hanime1.me 实际在 Cloudflare 上（A 记录 `104.26.x.x`/`172.67.x.x`），其 ECH 能力以干净 DoH（cloudflare-gateway 链）解析为准。目标域名解析靠 TXT 下发的 DoH（cloudflare-gateway 等，不经劫持链路）。
- alidns/360 JSON 端点是 `/resolve`；`/dns-query` 只支持 RFC 8484 二进制 POST。
- DoH 查询必须显式 `NO_PROXY`（否则 ECH 开启时系统代理指向本机代理 → 递归）。

### 1.4 【安全】gomobile 导出函数全部 `recover`

每个导出到 Android 的函数在入口用 `safe()` 包住，panic → 记录 + 返回错误，**绝不 abort 进程**（否则 Android 无限重启）。

### 1.5 崩溃 / 日志

- Android 端 `DiagnosticsLog` 持久化事件日志 + 崩溃自动写 Downloads（`Han1meViewer-crash-*.txt`）。
- Go 端 `Diagnostics()` 返回生命周期状态 + 有界日志（DNS 源 / ECH accept-reject / 降级 / 路由 / 上游错误）。

---

## 2. 排查连接问题时的检查清单

症状 → 先看哪个字段：

| 症状 | 看什么 |
|---|---|
| OkHttp 报 `Unable to parse TLS packet header` | 是不是 CONNECT 返回了已握手 TLS（违反 1.1） |
| 日志 `no ECHConfig for host, plain TLS` 但该 host 其实支持 ECH | 解析被污染（违反 1.3），核对 IP 是否 Facebook 段 |
| 直连真实 IP 超时 / Connection reset | 请求没走 ECH（拦截器未生效 / select 返回了代理），或该 host 本身不支持 ECH 且被墙 |
| App 无限重启 | gomobile panic 未 recover（违反 1.4），或 firebase-perf 占位 API key |
| Go 日志 `connected ... ech=true` 但 App 仍失败 | 接入模型用错（CONNECT 而非应用层），见 1.2 |

---

## 3. 架构速记

```
OkHttp(client) --应用层http + X-Ech-Target--> [ech-proxy-go] --DoH解析+ECH握手--> 上游(如javchu.com)
                 （返回明文响应）
WebView/MPV   --CONNECT 纯TCP隧道--> [ech-proxy-go] --TCP转发--> 上游
                 （客户端自己在隧道内 TLS）
```

---

## 4. 常用命令

```bash
export PATH=/tmp/go-toolchain/go/bin:$PATH   # 本机 go 工具链路径
gofmt -w <file> && go build ./... && go vet ./... && go test ./...
# CI 用 actions/checkout + setup-go + gomobile bind 编 AAR
```