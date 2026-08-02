# ECH Android UI Library

纯 Kotlin + Jetpack Compose 实现的 ECH（Encrypted Client Hello）代理配置 UI 组件。

## 特性

- ✅ DoH 端点管理（DNS over HTTPS）
- ✅ Cloudflare Edge IP 列表管理
- ✅ 远程配置同步（通过 TXT 记录）
- ✅ 连接测试
- ✅ 手动覆盖和自动恢复
- ✅ 零依赖（仅 AndroidX + Compose + OkHttp）

## 集成步骤

### 1. 作为 Library Module

在你的 Android 项目 `settings.gradle.kts` 中：

```kotlin
include(":ech-ui")
project(":ech-ui").projectDir = file("../ech-proxy-go/android-ui")
```

在 `build.gradle.kts` 中：

```kotlin
dependencies {
    implementation(project(":ech-ui"))
}
```

### 2. 使用 UI 组件

在你的 Compose 界面中：

```kotlin
import com.anglesgirl.echui.EchProxyManager
import com.anglesgirl.echui.EchSection

@Composable
fun SettingsScreen(context: Context) {
    val manager = remember { EchProxyManager(context) }
    
    Column {
        // 其他设置项...
        EchSection(manager = manager)
    }
}
```

### 3. 从 Go 代理读取配置

`EchProxyManager` 使用 `DataStore` 保存配置。从 Go 代理读取：

```kotlin
val dohFlow = manager.getDohFlow()
val ipsFlow = manager.getCustomIPsFlow()

dohFlow.collect { doh ->
    // 传递给 Go 代理或网络库
}
```

## 配置项

| 配置 | 默认值 | 说明 |
|------|--------|------|
| DoH | `https://pieqllv9i7.cloudflare-gateway.com/dns-query` | DNS over HTTPS 端点 |
| Config Domain | `ech-config.anglesgirl.eu.org` | 远程配置的 TXT 记录域名 |
| Custom IPs | 空 | Cloudflare Edge IP 列表（逗号分隔） |

## 远程配置格式

通过 TXT 记录返回配置（例如 `ech-config.anglesgirl.eu.org` 的 TXT 记录）：

```
doh=https://xxx.cloudflare-gateway.com/dns-query;doh2=https://yyy.cloudflare-gateway.com/dns-query;ip=104.20.8.2,104.20.9.2
```

## 兼容性

- **Min SDK:** 24
- **Target SDK:** 34
- **Compose:** 2023.10.00+
- **Kotlin:** 1.9.0+

## 依赖

- `androidx.datastore:datastore-preferences` - 配置持久化
- `com.squareup.okhttp3:okhttp` - HTTP 请求
- `androidx.compose.*` - UI 框架

## 许可证

MIT
