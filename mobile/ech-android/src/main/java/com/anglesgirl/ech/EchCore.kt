package com.anglesgirl.ech

import android.content.Context
import android.util.Log
import echproxy.Echproxy
import java.io.File
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.ServerSocket
import java.net.URL
import java.util.concurrent.Executors

/**
 * ECH 核心：种子 TXT 拉取 + 缓存 + 启动 Go 代理 + 热更新。
 * 完全不依赖接入方 App 的基础设施。
 *
 * 铁律（详见 ech-proxy-go/AGENTS.md 1.3）：
 *  - 种子只用 IP-DoH 查 TXT，绝不参与主站解析。
 *  - 启动顺序：种子 → 缓存兜底 → 启动 → 后台热更新。
 *  - 任何失败都不回退域名 DoH 污染源。
 */
internal object EchCore {

    private const val TAG = "EchCore"
    private const val REMOTE_CONFIG_DOMAIN = "ech-config.anglesgirl.eu.org"

    /** 多种子 IP-DoH（按顺序尝试）。2026-08-06 起移除 alidns，仅保留 IP 直连。 */
    private val SEED_DOH_LIST = listOf(
        "https://101.226.4.6/resolve",   // 360
        "https://120.53.53.53/resolve",  // 腾讯 DNSPod
        "https://1.12.12.12/resolve",    // 腾讯备用
    )

    private val scope = Executors.newSingleThreadExecutor()
    private var configCacheFile: File? = null

    fun start(context: Context) {
        try {
            configCacheFile = File(context.filesDir, "ech-remote-config.txt")
            val chosenPort = ServerSocket(0).use { it.localPort }
            val cachePath = File(context.filesDir, "ech-public-config.json").absolutePath

            val seed = runCatching { fetchRemoteConfig() }.getOrNull()
            val seedDoh = seed?.let { listOfNotNull(it.doh, it.doh2, it.doh3).distinct().joinToString(",") }
                ?.takeIf { it.isNotBlank() }
            val seedIp = seed?.ip?.takeIf { it.isNotBlank() }
            val seedOverride = seed?.override?.takeIf { it.isNotBlank() }
            if (seedDoh != null || seedIp != null || seedOverride != null) {
                saveConfigCache(seedDoh, seedIp, seedOverride)
                Ech.diagnostics?.event("ECH", "seed hit: doh=$seedDoh, ip=$seedIp, override=$seedOverride")
            }

            val cached = loadConfigCache()
            val dohArg = seedDoh ?: cached?.first
            val ipArg = seedIp ?: cached?.second ?: ""
            val overrideArg = seedOverride ?: cached?.third ?: ""

            // 都没有 → 断网（不启动 ECH），提示重启。
            if (dohArg.isNullOrBlank()) {
                Ech.diagnostics?.event("ECH", "no seed and no cache; ECH disabled (restart app)")
                Log.w(TAG, "no seed config and no cached DoH; ECH disabled")
                Ech.onPortChanged(-1)
                return
            }

            Ech.diagnostics?.event("ECH", "starting 127.0.0.1:$chosenPort (doh=$dohArg, ip=$ipArg)")
            runCatching {
                Echproxy.start("127.0.0.1:$chosenPort", dohArg, cachePath, false)
            }.onFailure { t ->
                Ech.diagnostics?.event("ECH", "native start failed; ECH disabled", t)
                Ech.onPortChanged(-1)
                return
            }

            Ech.onPortChanged(chosenPort)
            Ech.diagnostics?.event("ECH", "listening 127.0.0.1:$chosenPort; ${Echproxy.lastStatus()}")

            if (overrideArg.isNotBlank()) {
                runCatching { Echproxy.setOverrides(overrideArg) }
                    .onSuccess { Ech.diagnostics?.event("ECH", "overrides applied: $overrideArg") }
                    .onFailure { e -> Ech.diagnostics?.event("ECH", "overrides failed: ${e.message}") }
            }

            scope.execute { refreshRemoteConfig(dohArg, ipArg) }
        } catch (e: Throwable) {
            Ech.onPortChanged(-1)
            Ech.diagnostics?.event("ECH", "start failed; normal network kept", e)
            Log.e(TAG, "ECH start failed; keeping normal network", e)
        }
    }

    private fun refreshRemoteConfig(currentDoh: String, currentIp: String) {
        runCatching { fetchRemoteConfig() }
            .onSuccess { cfg ->
                val list = listOfNotNull(cfg.doh, cfg.doh2, cfg.doh3).distinct()
                val newDoh = if (list.isNotEmpty()) list.joinToString(",") else null
                val newIp = cfg.ip?.takeIf { it.isNotBlank() }
                val newOverride = cfg.override?.takeIf { it.isNotBlank() }
                saveConfigCache(newDoh, newIp, newOverride)
                if (newDoh != null || newIp != null) {
                    val finalDoh = newDoh ?: currentDoh
                    val finalIp = newIp ?: ""
                    if (finalDoh != currentDoh || finalIp != currentIp) {
                        runCatching { Echproxy.setEndpoints(finalDoh, finalIp) }
                            .onSuccess { Ech.diagnostics?.event("ECH", "endpoints hot-updated") }
                            .onFailure { e -> Ech.diagnostics?.event("ECH", "hot-update failed: ${e.message}") }
                    }
                }
                if (newOverride != null) {
                    runCatching { Echproxy.setOverrides(newOverride) }
                        .onSuccess { Ech.diagnostics?.event("ECH", "overrides hot-updated: $newOverride") }
                        .onFailure { e -> Ech.diagnostics?.event("ECH", "override hot-update failed: ${e.message}") }
                }
            }
            .onFailure { e -> Ech.diagnostics?.event("ECH", "refresh failed (cached/current): ${e.message}") }
    }

    // --- 种子 TXT ---

    private fun fetchRemoteConfig(): RemoteEchConfig {
        var lastError: Exception? = null
        for (seed in SEED_DOH_LIST) {
            try {
                val txt = dohQueryTxt(seed, REMOTE_CONFIG_DOMAIN)
                val cfg = parseRemoteConfig(txt)
                if (cfg.doh != null || cfg.ip != null) {
                    Ech.diagnostics?.event("ECH", "seed config via $seed")
                    return cfg
                }
            } catch (e: Exception) {
                lastError = e
                Log.w(TAG, "seed DoH failed via $seed: ${e.message}", e)
                Ech.diagnostics?.event("ECH", "seed DoH failed via $seed: ${e.message}", e)
            }
        }
        throw lastError ?: Exception("no seed DoH endpoint available")
    }

    private fun dohQueryTxt(doh: String, name: String): String {
        val u = URL("$doh?name=$name&type=TXT")
        val conn = u.openConnection(Proxy.NO_PROXY) as HttpURLConnection
        conn.requestMethod = "GET"
        conn.setRequestProperty("accept", "application/dns-json")
        conn.setRequestProperty("User-Agent", "EchSdk")
        conn.connectTimeout = 8000
        conn.readTimeout = 8000
        if (conn.responseCode != 200) throw Exception("seed DoH HTTP ${conn.responseCode} via $doh")
        return parseTxtJson(conn.inputStream.use { it.readBytes() }.decodeToString())
    }

    private fun parseTxtJson(json: String): String {
        val lines = mutableListOf<String>()
        val re = Regex("\"data\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\"")
        for (m in re.findAll(json)) {
            val raw = m.groupValues[1]
            lines.add(raw.replace("\\\"", "\"").replace("\\\\", "\\"))
        }
        if (lines.isEmpty()) throw Exception("no TXT records in DoH response")
        return lines.joinToString("\n")
    }

    private fun parseRemoteConfig(txt: String): RemoteEchConfig {
        val cfg = RemoteEchConfig()
        txt.split("\n").forEach { line ->
            line.split(";").forEach { part ->
                val idx = part.indexOf("=")
                if (idx > 0) {
                    val key = part.substring(0, idx).trim().trim('"').lowercase()
                    val value = part.substring(idx + 1).trim().trim('"')
                    when (key) {
                        "doh" -> cfg.doh = value
                        "doh2" -> cfg.doh2 = value
                        "doh3" -> cfg.doh3 = value
                        "ip", "ips" -> cfg.ip = value
                        "override" -> cfg.override = value
                    }
                }
            }
        }
        return cfg
    }

    private fun loadConfigCache(): Triple<String, String, String>? {
        val f = configCacheFile ?: return null
        return runCatching {
            val lines = f.readLines()
            if (lines.isEmpty() || lines[0].isBlank()) null
            else Triple(lines[0], lines.getOrNull(1) ?: "", lines.getOrNull(2) ?: "")
        }.getOrNull()
    }

    private fun saveConfigCache(doh: String?, ip: String?, override: String? = null) {
        val f = configCacheFile ?: return
        runCatching { f.writeText("${doh.orEmpty()}\n${ip.orEmpty()}\n${override.orEmpty()}") }
    }

    private data class RemoteEchConfig(
        var doh: String? = null,
        var doh2: String? = null,
        var doh3: String? = null,
        var ip: String? = null,
        var override: String? = null,
    )
}
