package com.anglesgirl.ech

import android.os.SystemClock
import okhttp3.HttpUrl
import okhttp3.Interceptor
import okhttp3.Request
import okhttp3.Response

/**
 * ECH 改写拦截器（纯无依赖版）。
 *
 * 把所有 HTTPS 请求改写为：
 *   http://127.0.0.1:<echPort>/<path> + X-Ech-Target:<host>
 * Go 代理统一处理 ECH / 普通 TLS。
 *
 * 注意：cookie 由 Ech.wrapOkHttp 注入的标准 JavaNetCookieJar 处理，
 * 不再依赖接入方专属 cookie jar。接入方自己的登录 cookie 应在本拦截器
 * 之前自行 add 拦截器叠加。
 *
 * 代理未启动（port<=0）时放行直连，避免全断。
 */
internal class EchInterceptor : Interceptor {

    private companion object {
        const val WAIT_ECH_TIMEOUT_MS = 10_000L
    }

    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val url = request.url

        var echPort = Ech.port
        if (echPort <= 0) {
            val deadline = SystemClock.elapsedRealtime() + WAIT_ECH_TIMEOUT_MS
            while (SystemClock.elapsedRealtime() < deadline) {
                echPort = Ech.port
                if (echPort > 0) break
                Thread.sleep(50)
            }
        }
        if (echPort <= 0) return chain.proceed(request)

        val originHost = url.host
        if (originHost.isBlank() ||
            originHost == "127.0.0.1" || originHost == "localhost" ||
            originHost.endsWith(".local")
        ) {
            return chain.proceed(request)
        }

        val proxyUrl = HttpUrl.Builder()
            .scheme("http")
            .host("127.0.0.1")
            .port(echPort)
            .encodedPath(url.encodedPath)
            .encodedQuery(url.encodedQuery ?: "")
            .build()

        val proxied = request.newBuilder()
            .url(proxyUrl)
            .header("X-Ech-Target", originHost)
            .header("Host", originHost)
            .build()

        Ech.diagnostics?.event("HTTP", "ECH route $originHost${url.encodedPath} -> 127.0.0.1:$echPort")
        return chain.proceed(proxied)
    }
}
