package com.anglesgirl.ech

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.webkit.CookieManager
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import echproxy.Echproxy
import okhttp3.JavaNetCookieJar
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Interceptor
import okhttp3.Response
import java.io.ByteArrayInputStream
import java.net.CookieManager as JavaCookieManager
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.ProxySelector
import java.net.ServerSocket
import java.net.URI
import java.util.concurrent.Executors

/**
 * ECH Android SDK —— 轻量可插拔接入层。
 *
 * 设计原则（详见 ech-proxy-go/docs/ech-android-sdk-design.md）：
 *  - 本模块 **不依赖任何接入方 App 的基础设施**（cookie jar / 诊断 / 埋点 / 设置）。
 *  - cookie 统一走 Android 系统 CookieManager（标准 JavaNetCookieJar），
 *    接入方自己的登录 cookie 由接入方在 wrapOkHttp 时自行叠加拦截器。
 *  - 诊断 / 埋点通过可选回调 [EchDiagnostics] 注入，不实现则空转。
 *  - 代理选择：ECH 开启时一律 NO_PROXY（X-Ech-Target 改写模式，禁止 CONNECT 隧道）；
 *    接入方自身代理逻辑通过 [appProxySelector] 兜底。
 *
 * 接入方改动量（从原 619 行胶水降到 ≤5 行）：
 *   Application.onCreate:        Ech.install(this)
 *   OkHttp.Builder 构建处:       Ech.wrapOkHttp(builder)
 *   WebView 初始化处:            Ech.wrapWebView(webView)
 */
object Ech {

    /** 可选诊断回调（接入方注入自己的日志/埋点）。 */
    @Volatile
    var diagnostics: EchDiagnostics? = null

    /** 接入方自身代理选择器（ECH 关闭时的兜底）。默认系统默认。 */
    @Volatile
    var appProxySelector: ProxySelector? = null

    private val executor = Executors.newSingleThreadExecutor()
    private val mainHandler = Handler(Looper.getMainLooper())

    /** 当前 ECH 代理端口；<=0 表示未启动。 */
    @Volatile
    var port: Int = -1
        private set

    val isRunning: Boolean
        get() = port > 0 && runCatching { Echproxy.isRunning() }.getOrDefault(false)

    /**
     * 应用启动时调用一次（替代原 EchProxyManager.startAsync）。
     * 内部：种子 IP-DoH 拉 TXT → 缓存/兜底 → 启动 Go 代理 → 热更新。
     */
    fun install(context: Context) {
        diagnostics?.event("ECH", "install requested; running=$isRunning")
        mainHandler.postDelayed({
            if (port > 0) return@postDelayed
            executor.execute { EchCore.start(context.applicationContext) }
        }, 500)
    }

    /** 停止 ECH 代理。 */
    fun uninstall() {
        executor.execute {
            runCatching { Echproxy.stop() }.onFailure { diagnostics?.event("ECH", "stop failed: ${it.message}") }
            port = -1
            rebuildNetwork()
        }
    }

    /**
     * 装饰 OkHttp.Builder：加入 ECH 改写拦截器 + 标准 CookieManager。
     * 接入方应在此之后自行 add 自己的登录/cookie 拦截器（如需）。
     */
    fun wrapOkHttp(builder: OkHttpClient.Builder): OkHttpClient.Builder {
        return builder
            .cookieJar(JavaNetCookieJar(JavaCookieManager.getInstance()))
            .addInterceptor(EchInterceptor())
    }

    /**
     * 装饰 WebView：设置 shouldInterceptRequest 走 ECH 代理。
     * 保留接入方原有 WebViewClient 行为（委托模式）。
     */
    fun wrapWebView(webView: WebView, delegate: WebViewClient? = null) {
        val echClient = EchWebViewClient(delegate)
        webView.webViewClient = echClient
    }

    // --- 内部：网络属性重建（WebView 系统代理 + ProxySelector） ---

    internal fun rebuildNetwork() {
        val props = System.getProperties()
        if (port > 0) {
            // ECH 开启：WebView 用同 loopback CONNECT 代理（与 OkHttp 一致）
            props["proxySet"] = "true"
            props["proxyHost"] = "127.0.0.1"
            props["proxyPort"] = port.toString()
        } else {
            props["proxySet"] = "false"
            props["proxyHost"] = ""
            props["proxyPort"] = ""
        }
    }

    /** 供 EchCore 设置端口后触发网络重建。 */
    internal fun onPortChanged(p: Int) {
        port = p
        rebuildNetwork()
    }
}
