package com.anglesgirl.ech

import java.io.IOException
import java.net.Proxy
import java.net.ProxySelector
import java.net.SocketAddress
import java.net.URI

/**
 * ECH 代理选择器（无依赖版）。
 *
 * 铁律（AGENTS.md 1.2）：ECH 开启时必须返回 Proxy.NO_PROXY，
 * 让 X-Ech-Target 改写模式直连本机代理，绝不可进入 CONNECT 隧道（无法隐藏 SNI）。
 *
 * ECH 关闭时委托给接入方自选的 [Ech.appProxySelector]（其自身系统/HTTP 代理逻辑），
 * 未设置则用系统默认。
 */
internal object EchProxySelector : ProxySelector() {

    private val fallback: ProxySelector
        get() = Ech.appProxySelector ?: getDefault() ?: NullSelector

    override fun select(uri: URI?): MutableList<Proxy> {
        if (Ech.port > 0) {
            return mutableListOf(Proxy.NO_PROXY)
        }
        return try {
            fallback.select(uri)
        } catch (e: Exception) {
            mutableListOf(Proxy.NO_PROXY)
        }
    }

    override fun connectFailed(uri: URI?, sa: SocketAddress?, ioe: IOException?) {
        runCatching { fallback.connectFailed(uri, sa, ioe) }
    }

    private object NullSelector : ProxySelector() {
        override fun select(uri: URI?): MutableList<Proxy> = mutableListOf(Proxy.NO_PROXY)
        override fun connectFailed(uri: URI?, sa: SocketAddress?, ioe: IOException?) {}
    }
}
