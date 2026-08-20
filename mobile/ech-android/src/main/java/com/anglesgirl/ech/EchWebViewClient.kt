package com.anglesgirl.ech

import android.webkit.CookieManager
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.ByteArrayInputStream

/**
 * WebView ECH 拦截（委托模式，保留接入方原有 WebViewClient 行为）。
 *
 * 所有子资源（HTML/JS/CSS/图片/接口）通过 OkHttp + EchInterceptor 走本地
 * ECH 代理（隐藏 SNI）。本机/内网请求交回 delegate 或 WebView 默认。
 *
 * cookie 同步：用 Android 系统 CookieManager（标准），不依赖接入方专属 jar。
 */
internal class EchWebViewClient(
    private val delegate: WebViewClient? = null
) : WebViewClient() {

    private val client: OkHttpClient by lazy {
        Ech.wrapOkHttp(OkHttpClient.Builder()).build()
    }

    override fun shouldInterceptRequest(
        view: WebView?,
        request: WebResourceRequest?
    ): WebResourceResponse? {
        if (Ech.port <= 0) return delegate?.shouldInterceptRequest(view, request)
        val url = request?.url ?: return delegate?.shouldInterceptRequest(view, request)
        val urlString = url.toString()
        val host = url.host ?: return delegate?.shouldInterceptRequest(view, request)
        if (host == "127.0.0.1" || host == "localhost" || host.endsWith(".local")) {
            return delegate?.shouldInterceptRequest(view, request)
        }

        return try {
            val okRequest = Request.Builder()
                .url(urlString)
                .method(request.method ?: "GET", null)
                .build()
            val resp = client.newCall(okRequest).execute()

            if (resp.code >= 400 && resp.code != 403) {
                resp.close()
                delegate?.shouldInterceptRequest(view, request)
            } else {
                val body = resp.body?.bytes() ?: ByteArray(0)
                val rawContentType = resp.header("Content-Type") ?: "text/html"
                val mime = rawContentType.substringBefore(";").trim()
                val charset = Regex("charset=([^;\\s\"']+)", RegexOption.IGNORE_CASE)
                    .find(rawContentType)?.groupValues?.get(1)?.trim() ?: "utf-8"

                val cm = CookieManager.getInstance()
                resp.headers("Set-Cookie").forEach { raw ->
                    runCatching { cm.setCookie(urlString, raw) }
                }
                runCatching { cm.flush() }

                WebResourceResponse(mime, charset, ByteArrayInputStream(body)).apply {
                    val headers = mutableMapOf<String, String>()
                    resp.headers.forEach { (name, value) ->
                        if (!name.equals("Content-Type", true) &&
                            !name.equals("Content-Encoding", true) &&
                            !name.equals("Content-Length", true)
                        ) {
                            headers[name] = value
                        }
                    }
                    responseHeaders = headers
                }
            }
        } catch (e: Exception) {
            delegate?.shouldInterceptRequest(view, request)
        }
    }

    // 把其他 WebViewClient 生命周期方法委托给原 client，保留接入方行为
    override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
        delegate?.onPageStarted(view, url, favicon)
    }

    override fun onPageFinished(view: WebView?, url: String?) {
        delegate?.onPageFinished(view, url)
    }

    override fun onReceivedError(view: WebView?, request: WebResourceRequest?, error: android.webkit.WebResourceError?) {
        delegate?.onReceivedError(view, request, error)
    }

    override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
        return delegate?.shouldOverrideUrlLoading(view, request) ?: false
    }
}
