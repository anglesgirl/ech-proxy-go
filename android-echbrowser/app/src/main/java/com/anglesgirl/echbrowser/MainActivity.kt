package com.anglesgirl.echbrowser

import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.ClipData
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.Gravity
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import org.mozilla.geckoview.GeckoRuntime
import org.mozilla.geckoview.GeckoRuntimeSettings
import org.mozilla.geckoview.GeckoSession
import org.mozilla.geckoview.GeckoView
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.Executors

/**
 * ECH 浏览器 Demo —— GeckoView（Firefox 内核）+ 本地 DoH 注入 ECH。
 *
 * 原理：
 *   1. App 内嵌 ech-doh Go 服务（127.0.0.1:8443 HTTPS DoH），启动时拉起
 *   2. 证书：doh.anglesgirl.eu.org 的 Let's Encrypt 合法证书（内嵌 assets）
 *   3. DNS：doh.anglesgirl.eu.org → 127.0.0.1（CF 托管，全球生效）
 *   4. GeckoView TRR mode=3 指向 https://doh.anglesgirl.eu.org:8443/dns-query
 *   5. 本地 DoH 对所有域名注入 CF 公共 ECH 公钥 → Firefox 原生 ECH 启用
 *      → SNI 隐藏 → 被墙站点（x.com 等）直接访问
 */
class MainActivity : AppCompatActivity() {

    private lateinit var geckoView: GeckoView
    private lateinit var session: GeckoSession
    private lateinit var address: EditText
    private lateinit var status: TextView
    private val io = Executors.newSingleThreadExecutor()
    private val logFile by lazy { File(filesDir, "echbrowser.log") }
    private val ts get() = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US).format(Date())

    private val DOH_DOMAIN = "doh.anglesgirl.eu.org"
    private val DOH_PORT = "8443"
    private val DOH_URL = "https://$DOH_DOMAIN:$DOH_PORT/dns-query"

    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        window.statusBarColor = Color.rgb(16, 20, 40)
        buildUi()
        log("APP", "onCreate; starting ech-doh + GeckoView")
        startEchDoh()
        startGecko()
    }

    /** 从 assets 读取内嵌证书，启动 Go DoH 注入服务。 */
    private fun startEchDoh() {
        io.execute {
            try {
                val cert = assets.open("doh-fullchain.pem").bufferedReader().readText()
                val key = assets.open("doh-key.pem").bufferedReader().readText()
                val err = com.anglesgirl.echbrowser.echdoh.Echdoh.start(
                    "127.0.0.1:$DOH_PORT", cert, key,
                    "https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query"
                )
                if (err != null) {
                    log("DOH", "start failed: $err")
                } else {
                    log("DOH", "ech-doh listening on $DOH_PORT (cert: $DOH_DOMAIN)")
                }
            } catch (e: Throwable) {
                log("DOH", "start exception: ${e.message}")
            }
        }
    }

    private fun buildUi() {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(8, 8, 8, 0)
            setBackgroundColor(Color.rgb(16, 20, 40))
        }
        val bar = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        address = EditText(this).apply {
            hint = "输入网址"
            setText("https://x.com")
            setTextColor(Color.WHITE)
            setHintTextColor(Color.GRAY)
            setSingleLine(true)
        }
        bar.addView(address, LinearLayout.LayoutParams(0, 52, 1f))
        fun button(text: String, click: () -> Unit) = Button(this).apply {
            this.text = text
            setOnClickListener { click() }
        }
        bar.addView(button("打开") { load() })
        bar.addView(button("日志") { showLogs() })
        root.addView(bar)

        val actions = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        actions.addView(button("后退") { session.goBack() })
        actions.addView(button("前进") { session.goForward() })
        actions.addView(button("刷新") { session.reload() })
        actions.addView(button("导出") { exportLogs() })
        root.addView(actions)

        status = TextView(this).apply {
            setTextColor(Color.LTGRAY)
            textSize = 11f
            text = "启动中: ech-doh + GeckoView..."
            setPadding(6, 2, 6, 2)
        }
        root.addView(status)

        geckoView = GeckoView(this)
        root.addView(geckoView, LinearLayout.LayoutParams(-1, 0, 1f))
        setContentView(root)
    }

    /** GeckoView 初始化：TRR 指向本地 DoH（注入 ECH 的关键）。 */
    @SuppressLint("WrongThread")
    private fun startGecko() {
        val settings = GeckoRuntimeSettings.Builder()
            .javaScriptEnabled(true)
            // 仅用 DoH（TRR_MODE_ONLY），指向本地注入服务器
            .trustedRecursiveResolverMode(GeckoRuntimeSettings.TRR_MODE_ONLY)
            .trustedRecursiveResolverUri(DOH_URL)
            .build()
        val runtime = GeckoRuntime.create(this, settings)
        session = GeckoSession()

        session.navigationDelegate = object : GeckoSession.NavigationDelegate {
            override fun onLocationChange(
                session: GeckoSession,
                url: String?,
                permissions: List<GeckoSession.PermissionDelegate.ContentPermission>,
                hasUserGesture: Boolean
            ) {
                log("NAV", "location=$url")
                runOnUiThread {
                    address.setText(url ?: "")
                    status.text = "地址: ${url ?: ""}"
                }
            }

            override fun onCanGoBack(s: GeckoSession, canGoBack: Boolean) {}
            override fun onCanGoForward(s: GeckoSession, canGoForward: Boolean) {}
        }

        session.progressDelegate = object : GeckoSession.ProgressDelegate {
            override fun onPageStart(s: GeckoSession, url: String) {
                log("PAGE", "start=$url")
                setStatus("加载中...")
            }

            override fun onPageStop(s: GeckoSession, success: Boolean) {
                log("PAGE", "stop success=$success")
                setStatus(if (success) "✅ 加载完成" else "❌ 加载失败")
            }

            override fun onSecurityChange(
                s: GeckoSession,
                info: GeckoSession.ProgressDelegate.SecurityInformation
            ) {
                log("TLS", "security=${info.origin} secure=${info.isSecure}")
            }
        }

        session.open(runtime)
        geckoView.setSession(session)
        setStatus("ech-doh + GeckoView 就绪")
        load()
    }

    private fun load() {
        val raw = address.text.toString().trim()
        val url = if (raw.startsWith("http")) raw else "https://$raw"
        log("USER", "load=$url")
        session.loadUri(url)
    }

    private fun setStatus(s: String) {
        runOnUiThread { status.text = s }
    }

    private fun log(tag: String, msg: String) {
        val line = "$ts [$tag] $msg\n"
        io.execute {
            try {
                logFile.parentFile?.mkdirs()
                logFile.appendText(line)
            } catch (_: Throwable) {}
        }
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
        ScrollableDialog(this, view, "ECH 浏览器日志")
    }

    private fun ScrollableDialog(ctx: AppCompatActivity, view: TextView, title: String) {
        val sv = android.widget.ScrollView(ctx).apply { addView(view) }
        AlertDialog.Builder(ctx)
            .setTitle(title)
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
        val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", logFile)
        val i = Intent(Intent.ACTION_SEND).apply {
            type = "text/plain"
            putExtra(Intent.EXTRA_STREAM, uri)
            clipData = ClipData.newRawUri("日志", uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        startActivity(Intent.createChooser(i, "导出 ECH 浏览器日志"))
    }

    override fun onDestroy() {
        log("APP", "onDestroy")
        io.shutdown()
        try {
            session.close()
            com.anglesgirl.echbrowser.echdoh.Echdoh.stop()
        } catch (_: Throwable) {}
        super.onDestroy()
    }
}
