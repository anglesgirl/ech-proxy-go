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

### 1.3 【铁律】种子协议：种子只用 IP-DoH，只做 TXT 获取，绝不参与主站解析

**这是整套 ECH 能工作的根基。违反任何一条都会立刻被污染、连不上，必须整体遵守，不可"临时兜底"偏航。**

#### 1.3.1 种子定义（三件事，无第四件）

种子的**唯一职责**是从 `ech-config.anglesgirl.eu.org` 的 **TXT 记录**拉取配置：
`doh=` / `doh2=` / `doh3=` / `ip=`（ip 为优选边缘 IP）。除此之外**什么都不做**。

三个铁律：
1. **种子只用 IP 直连格式的 DoH**。候选列表（全部必须是 `https://<IP>/resolve`，禁止任何域名形式）：
   ```
   https://223.5.5.5/resolve     阿里 alidns  IP
   https://101.226.4.6/resolve   360         IP
   https://120.53.53.53/resolve  腾讯 DNSPod IP
   https://223.6.6.6/resolve     阿里备用    IP
   ```
   **禁止 `doh.pub`、`dns.alidns.com`、`cloudflare-dns.com` 等域名做种子**——域名解析环节可被劫持（劫持后返回的 TXT 全是伪造的）。IP 直连跳过该环节，从源头防劫持。
2. **种子只做 TXT 获取**。它查完 TXT、把配置交给代理后，**任务即结束**。种子的 IP-DoH **绝不用于解析主站/CDN/IP**（那些属于污染源）。
3. **主站与目标 IP 的解析一律用 TXT 下发的 DoH**（即 `doh=`/`doh2=`/`doh3=`，通常是 cloudflare-gateway 链）。不用 TXT 下发的 DoH、回头用 alidns/doh.pub/任何种子去解析主站 = **立即污染**。

#### 1.3.2 启动顺序（铁律，不可"先启动再补丁"）

```
1. 启动 APP
2. 等待 ECH 代理启动窗口
3. ECH 从种子（纯 IP-DoH）获取配置（TXT）→ 拿到 doh/doh2/doh3/ip
4. 缓存配置（优选 IP 等）供下次冷启动直接使用
5. 用 TXT 下发的 DoH 启动/接受 App 连接
```

**严禁**：先拿 alidns/默认 DoH 启动、再后台热更新换掉。首请求发生在那之前 = 首请求走污染源 = 假 `no ECHConfig` / 假 IP / 超时（本次日志 `starting ... doh=https://dns.alidns.com/resolve` 即此错误）。

#### 1.3.3 兜底策略（fail-closed，绝不死回污染源）

- 种子成功 → 用 TXT 配置启动。
- 种子全失败 → 用**上次缓存的优选 IP / DoH**（缓存即是上次成功的 TXT 结果）启动。
- 种子失败且无缓存 → **断网（不启动 ECH）**，日志提示用户重启 App 重试。
- **任何情况下都不允许退回 `dns.alidns.com` / `doh.pub` 等域名 DoH 兜底**——那等于自杀式污染。

#### 1.3.4 技术要点

- DoH 查询必须显式 `Proxy.NO_PROXY`（ECH 开启时系统代理指向本机代理 → 否则递归）。
- alidns/360/腾讯 IP 的 JSON 端点是 `/resolve`；`/dns-query` 只支持 RFC 8484 二进制 POST。
- TXT 的 `doh=` 通常是 **cloudflare-gateway**（大陆可能被墙）。
- IP 直连种子只防**端点劫持**；目标域名本身的污染（如 alidns 曾给 hanime1.me 返回 Facebook 段假 IP `31.13.84.x`）靠 **TXT 下发的 DoH** 解析规避。

### 1.4 【安全】gomobile 导出函数全部 `recover`

每个导出到 Android 的函数在入口用 `safe()` 包住，panic → 记录 + 返回错误，**绝不 abort 进程**（否则 Android 无限重启）。

### 1.5 【关键】ECH 只支持 TLS 1.3；plain-TLS 目标放宽到 TLS 1.2

- **ECH 握手连接必须强制 `MinVersion: tls.VersionTLS13`**（ECH 仅在 TLS 1.3 中定义）。`internal/tlsconn/dialer.go`：仅当目标**有 ECH 配置**时才设 `MinVersion=TLS13`；**无 ECH（plain TLS）时用 `MinVersion=TLS12`**，以兼容内容页里只支持 TLS 1.2 的老 CDN/静态源站。ECH/plain 的切换由 `hasECH` 决定。
- **ALPN 会协商成 `h2`**：应用层转发（`appLayerClient`）的 `http.Transport` **必须允许 HTTP/2**，禁止设 `ForceAttemptHTTP2: false`——否则服务器按 HTTP/2 发帧、Go 按 HTTP/1 解析 → `malformed HTTP response "\x00\x00\x12\x04..."`（SETTINGS 帧）。
- CONNECT 隧道给 WebView/MPV 纯 TCP 转发时不涉及（客户端自己定协议）。

### 1.6 崩溃 / 日志

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
| 日志 `starting ... doh=https://dns.alidns.com/resolve` | 启动兜底用了污染源（违反 1.3.2/1.3.3），首请求被污染 |
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