package com.anglesgirl.echbrowser

import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.ClipData
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.Gravity
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import org.mozilla.geckoview.GeckoRuntime
import org.mozilla.geckoview.GeckoRuntimeSettings
import org.mozilla.geckoview.GeckoSession
import org.mozilla.geckoview.GeckoView
import org.mozilla.geckoview.WebExtension
import org.mozilla.geckoview.WebRequestError
import java.io.File

/**
 * ECH 浏览器 —— GeckoView（Firefox 内核）+ 本地 DoH 注入 ECH。
 *
 * 基于已验证可运行的 ao3-kiosk 工程配置（GeckoView 147 + configFilePath YAML prefs）。
 * 区别：TRR 指向本地 DoH 注入服务器（doh.anglesgirl.eu.org -> 127.0.0.1），
 * 对所有域名注入 CF 公共 ECH 公钥 → Firefox 原生 ECH 隐藏 SNI。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var geckoView: GeckoView
    private lateinit var session: GeckoSession
    private lateinit var urlBar: EditText
    private lateinit var status: TextView
    private var runtime: GeckoRuntime? = null

    private val DOH_DOMAIN = "doh.anglesgirl.eu.org"
    private val DOH_PORT = "8443"
    private val DOH_URI = "https://$DOH_DOMAIN:$DOH_PORT/dns-query"
    private val logFile by lazy { File(filesDir, "echbrowser.log") }

    override fun onCreate(savedInstanceState: Bundle?) {
        log("APP", "onCreate ENTER")
        super.onCreate(savedInstanceState)
        log("APP", "onCreate super done")
        buildUi()
        log("APP", "UI built")
        startEchDoh()
        startGoLogPoller()
        startGecko()
    }

    /** 轮询 Go 侧 DoH 日志（注入/改写过程），写入 echbrowser.log。 */
    private fun startGoLogPoller() {
        Thread {
            while (!isFinishing) {
                try {
                    val logs = com.anglesgirl.echbrowser.echdoh.Echdoh.pollLogs()
                    if (logs.isNotEmpty()) {
                        for (line in logs.split("\n")) {
                            if (line.isNotBlank()) log("DOH", line)
                        }
                    }
                } catch (_: Throwable) {
                }
                Thread.sleep(1000)
            }
        }.apply { name = "golog"; start() }
    }

    private fun buildUi() {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.rgb(16, 20, 40))
            fitsSystemWindows = true
        }

        val bar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(8, 4, 8, 4)
        }
        urlBar = EditText(this).apply {
            hint = "输入网址"
            setText("https://x.com")
            setTextColor(Color.WHITE)
            setHintTextColor(Color.GRAY)
            setSingleLine(true)
            setPadding(8, 0, 8, 0)
        }
        bar.addView(urlBar, LinearLayout.LayoutParams(0, 48, 1f))
        fun button(text: String, click: () -> Unit) = Button(this).apply {
            this.text = text
            setOnClickListener { click() }
        }
        bar.addView(button("打开") { loadUrl() })
        bar.addView(button("日志") { showLogs() })
        root.addView(bar)

        val actions = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(8, 0, 8, 4)
        }
        actions.addView(button("后退") { session.goBack() })
        actions.addView(button("前进") { session.goForward() })
        actions.addView(button("刷新") { session.reload() })
        actions.addView(button("IP设置") { showOverrideDialog() })
        actions.addView(button("导出") { exportLogs() })
        root.addView(actions)

        status = TextView(this).apply {
            setTextColor(Color.LTGRAY)
            textSize = 11f
            text = "启动中..."
            setPadding(8, 2, 8, 2)
        }
        root.addView(status)

        geckoView = GeckoView(this)
        root.addView(geckoView, LinearLayout.LayoutParams(-1, 0, 1f))
        setContentView(root)
    }

    private fun startEchDoh() {
        Thread {
            try {
                log("DOH", "reading certs...")
                // 手动 IP 覆盖（2026-08-15 用户要求）：启动时应用上次保存的
                // "域名=IP" 规则，不用等构建直接测试任意 IP。
                try {
                    val saved = getSharedPreferences("echbrowser", MODE_PRIVATE)
                        .getString("override", "") ?: ""
                    if (saved.isNotBlank()) {
                        com.anglesgirl.echbrowser.echdoh.Echdoh.setOverride(saved.replace("\n", ","))
                        log("DOH", "override loaded: $saved")
                    }
                } catch (e: Throwable) {
                    log("DOH", "override load failed: ${e.message}")
                }
                // 加载域名探测缓存（可强改名单自动学习）
                try {
                    com.anglesgirl.echbrowser.echdoh.Echdoh.loadProbeCache(
                        File(filesDir, "probe-cache.json").absolutePath
                    )
                } catch (_: Throwable) {}
                val cert = assets.open("doh-fullchain.pem").bufferedReader().readText()
                val key = assets.open("doh-key.pem").bufferedReader().readText()
                log("DOH", "cert ${cert.length}B key ${key.length}B")
                try {
                    com.anglesgirl.echbrowser.echdoh.Echdoh.start(
                        "127.0.0.1:$DOH_PORT", cert, key,
                        "https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query"
                    )
                    log("DOH", "start() returned OK")
                } catch (e: Throwable) {
                    log("DOH", "start() threw: ${e.message}")
                }
                Thread.sleep(1000)
                // 打印服务内部错误（ServeTLS 失败等 goroutine 内错误）
                val lastErr = com.anglesgirl.echbrowser.echdoh.Echdoh.lastError()
                log("DOH", "LastError: ${if (lastErr.isNullOrEmpty()) "(none)" else lastErr}")
                // 等待 CF IP 扫描完成（最多 15s），打印轮换池
                for (i in 0..15) {
                    val pool = com.anglesgirl.echbrowser.echdoh.Echdoh.reachableCFIPsForTest()
                    if (pool.isNotBlank()) {
                        log("DOH", "CF IP pool ($DOH_PORT reachable):\n${pool.trim()}")
                        break
                    }
                    Thread.sleep(1000)
                }
                try {
                    val s = java.net.Socket("127.0.0.1", DOH_PORT.toInt())
                    s.close()
                    log("DOH", "health: port $DOH_PORT OPEN ✓")
                } catch (e: Throwable) {
                    log("DOH", "health: port $DOH_PORT CLOSED ✗ ($e)")
                }
            } catch (e: Throwable) {
                log("DOH", "exception: ${e.message}")
            }
        }.apply { name = "echdoh"; start() }
    }

    /** GeckoView 初始化：configFilePath YAML prefs 配 TRR（ao3-kiosk 同款方式）。 */
    @SuppressLint("WrongThread")
    private fun startGecko() {
        try {
            log("GECKO", "writing config yaml...")
            val configYaml = buildString {
                appendLine("prefs:")
                appendLine("  network.trr.mode: 3")
                appendLine("  network.trr.uri: \"$DOH_URI\"")
                appendLine("  network.trr.excluded-domains: \"\"")
                // 对齐桌面 Firefox 验证成功配置（2026-08-14 桌面 x.com 打开）
                // 不设 echloop / use_https_rr_as_altns（ao3-kiosk 的 AO3 专用，
                // 对注入 ech= 场景疑似副作用）
                appendLine("  dom.security.https_only_mode: true")
            }
            val configFile = File(filesDir, "geckoview-config.yaml")
            configFile.writeText(configYaml)
            log("GECKO", "config written: $configYaml")

            val settings = GeckoRuntimeSettings.Builder()
                .configFilePath(configFile.absolutePath)
                .build()
            log("GECKO", "creating runtime...")
            runtime = GeckoRuntime.create(this, settings)
            log("GECKO", "runtime created")

            session = GeckoSession()
            session.navigationDelegate = object : GeckoSession.NavigationDelegate {
                override fun onLocationChange(
                    session: GeckoSession,
                    url: String?,
                    permissions: List<GeckoSession.PermissionDelegate.ContentPermission>,
                    hasUserGesture: Boolean
                ) {
                    log("NAV", "location=$url")
                    runOnUiThread { urlBar.setText(url ?: "") }
                }

                override fun onLoadError(
                    s: GeckoSession,
                    uri: String?,
                    error: WebRequestError
                ): org.mozilla.geckoview.GeckoResult<String>? {
                    log("ERR", "loadError uri=$uri code=${error.code} msg=${error.message}")
                    return null
                }

                override fun onCanGoBack(s: GeckoSession, canGoBack: Boolean) {}
                override fun onCanGoForward(s: GeckoSession, canGoForward: Boolean) {}
            }
            session.progressDelegate = object : GeckoSession.ProgressDelegate {
                override fun onPageStart(s: GeckoSession, url: String) {
                    log("PAGE", "start=$url")
                    runOnUiThread { status.text = "加载中... $url" }
                }

                override fun onPageStop(s: GeckoSession, success: Boolean) {
                    log("PAGE", "stop success=$success")
                    runOnUiThread {
                        status.text = if (success) "✅ 完成" else "❌ 失败"
                    }
                }

                override fun onSecurityChange(
                    s: GeckoSession,
                    info: GeckoSession.ProgressDelegate.SecurityInformation
                ) {
                    log("TLS", "security=${info.origin} secure=${info.isSecure}")
                }
            }

            log("GECKO", "opening session...")
            session.open(runtime!!)
            geckoView.setSession(session)
            log("GECKO", "session open, loading")
            installTwimgRewrite()
            loadUrl()
        } catch (e: Throwable) {
            log("GECKO", "FAILED: $e")
            runOnUiThread { status.text = "❌ GeckoView 失败: ${e.message}" }
        }
    }

    /** 内置扩展：把 CF 上没有配置的 twimg 子域（abs-0 等）重写到
     *  abs.twimg.com。请求发出前在扩展层改 host，零 SNI 泄漏、零额外
     *  往返。2026-08-15：abs-0 在 CF 无配置（明文 SNI 打 CF 边缘握手
     *  失败、Host=abs-0 返回 403），只能 fail-closed 或改写。内容实测
     *  等价：同路径 624557 字节仅差 40 字节 Sentry release id。
     *  失败不影响主流程 —— 只是资源缺失，不该让浏览器起不来。 */
    private fun installTwimgRewrite() {
        val rt = runtime ?: return
        // GeckoResult.accept(Consumer<T>, Consumer<Throwable>)：两个独立的
        // Consumer 参数，分别处理成功和失败。不用 exceptionally（它的
        // listener 要返回 GeckoResult<U>，裸值不匹配）。
        rt.webExtensionController.installBuiltIn("resource://android/assets/twimg-rewrite/")
            .accept(
                { ext: WebExtension? ->
                    log("EXT", "twimg-rewrite installed: ${ext?.id}")
                },
                { e: Throwable? ->
                    log("EXT", "twimg-rewrite install failed: $e")
                }
            )
    }

    /** 手动 IP 覆盖对话框（2026-08-15 用户要求）：输"域名=IP"每行一条，
     *  应用后热更新（无需重启），下次启动自动加载。测试任意 IP 不用等构建。 */
    private fun showOverrideDialog() {
        val prefs = getSharedPreferences("echbrowser", MODE_PRIVATE)
        val input = EditText(this).apply {
            setText(prefs.getString("override", "") ?: "")
            hint = "域名=IP，每行一条\n例如：\nx.com=162.159.140.229"
            setTextColor(Color.WHITE)
            setHintTextColor(Color.GRAY)
            gravity = Gravity.TOP
            setSingleLine(false)
            minLines = 4
        }
        AlertDialog.Builder(this)
            .setTitle("手动 IP 覆盖")
            .setView(input)
            .setPositiveButton("应用") { _, _ ->
                val v = input.text.toString().trim()
                prefs.edit().putString("override", v).apply()
                try {
                    com.anglesgirl.echbrowser.echdoh.Echdoh.setOverride(v.replace("\n", ","))
                    log("DOH", "override applied: $v")
                    Toast.makeText(this, "已应用，刷新页面生效", Toast.LENGTH_SHORT).show()
                } catch (e: Throwable) {
                    Toast.makeText(this, "应用失败: ${e.message}", Toast.LENGTH_LONG).show()
                }
            }
            .setNegativeButton("清空", { _, _ ->
                prefs.edit().remove("override").apply()
                try {
                    com.anglesgirl.echbrowser.echdoh.Echdoh.setOverride("")
                    Toast.makeText(this, "已清空覆盖", Toast.LENGTH_SHORT).show()
                } catch (_: Throwable) {}
            })
            .setNeutralButton("取消", null)
            .show()
    }

    private fun loadUrl() {
        val raw = urlBar.text.toString().trim()
        val url = if (raw.startsWith("http")) raw else "https://$raw"
        log("USER", "load=$url")
        try {
            session.loadUri(url)
        } catch (e: Throwable) {
            log("USER", "load FAILED: $e")
        }
    }

    private fun log(tag: String, msg: String) {
        val line = "${java.text.SimpleDateFormat("HH:mm:ss.SSS", java.util.Locale.US).format(java.util.Date())} [$tag] $msg\n"
        try {
            logFile.parentFile?.mkdirs()
            logFile.appendText(line)
        } catch (_: Throwable) {}
        android.util.Log.i("EchBrowser", line.trim())
    }

    private fun showLogs() {
        val text = if (logFile.exists()) logFile.readText() else "暂无日志"
        val view = TextView(this).apply {
            setTextColor(Color.WHITE)
            textSize = 11f
            setPadding(20, 10, 20, 10)
            this.text = text
        }
        val sv = ScrollView(this).apply { addView(view) }
        AlertDialog.Builder(this)
            .setTitle("ECH 浏览器日志")
            .setView(sv)
            .setPositiveButton("关闭", null)
            .setNeutralButton("导出") { _, _ -> exportLogs() }
            .show()
    }

    private fun exportLogs() {
        if (!logFile.exists()) {
            Toast.makeText(this, "暂无日志", Toast.LENGTH_SHORT).show()
            return
        }
        try {
            val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", logFile)
            val i = Intent(Intent.ACTION_SEND).apply {
                type = "text/plain"
                putExtra(Intent.EXTRA_STREAM, uri)
                clipData = ClipData.newRawUri("日志", uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(i, "导出日志"))
        } catch (e: Throwable) {
            Toast.makeText(this, "导出失败: ${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    override fun onDestroy() {
        log("APP", "onDestroy")
        try {
            session.close()
            com.anglesgirl.echbrowser.echdoh.Echdoh.stop()
        } catch (_: Throwable) {}
        super.onDestroy()
    }
}
