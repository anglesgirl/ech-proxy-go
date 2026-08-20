# ECH Android SDK 轻量化设计评审

> 目标：把 ECH 做成独立、轻量、可插拔的 Android 模块。接入第三方 App 时只改 ≤5 行；对方 App 升级时我们升 aar 版本号即可同步，不再大改一批代码。

## 1. 现状诊断（基于 Han1meViewer-ECH 实际代码）

### 已有的好基础
- `ech-proxy-go/mobile/echproxy` 编译为 `echproxy.aar`（3 个 Go 文件：echproxy.go / probe_stream.go / xprobe.go），CI 自动从 ech-proxy-go 构建并落到 `app/libs/echproxy.aar`
- 核心代理逻辑（Go 原生 ECH/DoH/override）已隔离在 aar 内，App 侧只有薄封装

### 真痛点（升级冲突根源）
App 侧 4 个 Kotlin 胶水类共 **619 行**，需 hook 进 App 的三层网络栈：

| 文件 | 行数 | 职责 | hook 点 |
|---|---|---|---|
| `logic/ech/EchProxyManager.kt` | 315 | 生命周期/配置下发/启动 | Application.onCreate |
| `logic/network/HProxySelector.kt` | 116 | 系统代理选择器 | 网络栈初始化 |
| `logic/network/EchInterceptor.kt` | 98 | OkHttp 拦截器（注入 ECH/override） | OkHttp.Builder |
| `logic/network/EchWebViewClient.kt` | 90 | WebView 客户端（ECH 页面加载） | WebView 设置 |

41 个文件"引用" ECH，但**真正调 API 的只有 4 处**：
1. `HanimeApplication.kt:54` — `EchProxyManager.startAsync(this)`
2. `NetworkSettingsScreen.kt:403` — 设置页启动
3. `HomePageViewModel.kt:102/108` — 首页重试逻辑
4. `HMediaKernel.kt` — 视频播放网络

**结论**：胶水不是"散在 41 文件"，而是这 4 个 kt 类要侵入 App 的 OkHttp/WebView/代理三层初始化。对方 App 一升级，这些初始化代码必冲突 → 需手动 rebase 一批。

## 2. 轻量化目标形态

把 4 个 kt 胶水类**编译进 aar**（aar 同时含 Go .so + Kotlin 封装层），对外只暴露 3 个入口：

```kotlin
// 1. 应用启动时（替代 HanimeApplication 那行）
Ech.install(context)

// 2. OkHttp 构建时（装饰器，替代手动加 EchInterceptor + HProxySelector）
Ech.wrapOkHttp(OkHttp.Builder())

// 3. WebView 初始化时（装饰器，替代自定义 EchWebViewClient）
Ech.wrapWebView(webView)
```

接入方工作量从"改 40+ 文件 / 619 行胶水"降到"改 3-5 行"。

### 配置下发边界（不改）
- seed TXT / override / DoH 域名等仍由远端下发（`ech-proxy-go` 服务端 + `101.226.4.6` / `res.anglesgirl.eu.org`）
- aar 内部拉取，接入方零配置
- 版本号随 aar 发布，升级只需换 aar

## 3. 模块边界

```
ech-proxy-go/
├── mobile/echproxy/        # 现有 Go 核心 → .so
├── mobile/echproxy-android/ # 【新增】Kotlin 封装层（含原 4 个胶水类）
│   ├── Ech.kt              # 对外门面：install/wrapOkHttp/wrapWebView
│   ├── EchProxyManager.kt  # 原 logic/ech 内容
│   ├── EchInterceptor.kt   # 原 logic/network 内容
│   ├── HProxySelector.kt
│   └── EchWebViewClient.kt
└── build_aar.sh            # 打包 Go .so + Kotlin 层 → echproxy.aar
```

## 4. 排期

| 阶段 | 内容 | 交付物 | 状态 |
|---|---|---|---|
| P0 | 设计评审 + API 契约冻结 | 本文档 | ✅ 完成 |
| P1 | 把 4 个 kt 胶水移入 `mobile/echproxy-android` 编译进 aar | aar v2 | 待启动 |
| P2 | 写 Han1meViewer 接入样例 diff（证明 ≤5 行）+ 独立 demo App | 模板仓库 | 待启动 |
| P3 | 模拟"上游更新"：rebase 新版 Han1meViewer，确认只改 aar 版本号 | 验证报告 | 待启动 |

## 5. 风险与决策点
- **aar 内 Kotlin 层与接入方 Kotlin 版本冲突**：用 `api`/`implementation` 隔离，封装层不暴露第三方依赖
- **多 App 共用同一 seed/配置**：配置按 App 包名隔离（aar 内区分），避免串号
- **WebView 装饰器兼容性**：不同 App 自定义 WebViewClient 可能冲突，wrapWebView 用委托模式保留原 client 行为
- **升级验证标准**：对方 App 升一版后，我们 PR 只改 `implementation(files("libs/echproxy.aar"))` 的 aar 文件 + 版本号，不碰业务代码

## 6. 验收标准
- [ ] 新接入一个空白 Demo App，ECH 全功能跑通，改动 ≤5 行
- [ ] Han1meViewer 回退到"仅 4 处调用"形态，原 619 行胶水删除
- [ ] 模拟上游更新：rebase 后只改 aar 版本号即编译通过
