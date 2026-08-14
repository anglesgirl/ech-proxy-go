package com.anglesgirl.echbrowser

import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.ClipData
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.provider.MediaStore
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
import java.io.File

/**
 * ECH 浏览器 Demo —— GeckoView（Firefox 内核）+ 本地 DoH 注入 ECH。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var geckoView: GeckoView
    private lateinit var session: GeckoSession
    private lateinit var address: EditText
    private lateinit var status: TextView

    private val DOH_DOMAIN = "doh.anglesgirl.eu.org"
    private val DOH_PORT = "8443"
    private val DOH_URL = "https://$DOH_DOMAIN:$DOH_PORT/dns-query"

    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        EchApp.log("APP", "MainActivity.onCreate start")
        try {
            window.statusBarColor = Color.rgb(16, 20, 40)
            buildUi()
            EchApp.log("APP", "UI built")
        } catch (e: Throwable) {
            EchApp.log("APP", "buildUi FAILED: $e")
            EchApp.crash(Thread.currentThread(), e)
        }

        // 先启动本地 DoH（异步），再初始化 GeckoView
        startEchDoh()
        try {
            startGecko()
            EchApp.log("APP", "GeckoView started OK")
        } catch (e: Throwable) {
            EchApp.log("APP", "startGecko FAILED: $e")
            EchApp.crash(Thread.currentThread(), e)
            setStatus("❌ GeckoView 初始化失败: ${e.message}")
        }
    }

    /** 启动 Go DoH 注入服务。 */
    private fun startEchDoh() {
        Thread {
            try {
                EchApp.log("DOH", "reading certs from assets...")
                val cert = assets.open("doh-fullchain.pem").bufferedReader().readText()
                val key = assets.open("doh-key.pem").bufferedReader().readText()
                EchApp.log("DOH", "cert ${cert.length}B key ${key.length}B loaded")
                val err = com.anglesgirl.echbrowser.echdoh.Echdoh.start(
                    "127.0.0.1:$DOH_PORT", cert, key,
                    "https://pieqllv9i7.cloudflare-gateway.com/dns-query,https://162.159.36.5/dns-query"
                )
                if (err != null) {
                    EchApp.log("DOH", "start failed: $err")
                } else {
                    EchApp.log("DOH", "ech-doh listening on $DOH_PORT (cert: $DOH_DOMAIN)")
                }
            } catch (e: Throwable) {
                EchApp.log("DOH", "start exception: ${e.message}")
                EchApp.crash(Thread.currentThread(), e)
            }
        }.apply { name = "echdoh-start"; start() }
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

    /** GeckoView 初始化：TRR 指向本地 DoH。 */
    @SuppressLint("WrongThread")
    private fun startGecko() {
        EchApp.log("GECKO", "building runtime settings...")
        val settings = GeckoRuntimeSettings.Builder()
            .javaScriptEnabled(true)
            .trustedRecursiveResolverMode(GeckoRuntimeSettings.TRR_MODE_ONLY)
            .trustedRecursiveResolverUri(DOH_URL)
            .build()
        EchApp.log("GECKO", "runtime settings built, creating runtime...")

        val runtime = GeckoRuntime.create(this, settings)
        EchApp.log("GECKO", "runtime created: ${runtime.javaClass.name}")
        session = GeckoSession()
        EchApp.log("GECKO", "session created")

        session.navigationDelegate = object : GeckoSession.NavigationDelegate {
            override fun onLocationChange(
                session: GeckoSession,
                url: String?,
                permissions: List<GeckoSession.PermissionDelegate.ContentPermission>,
                hasUserGesture: Boolean
            ) {
                EchApp.log("NAV", "location=$url")
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
                EchApp.log("PAGE", "start=$url")
                setStatus("加载中...")
            }

            override fun onPageStop(s: GeckoSession, success: Boolean) {
                EchApp.log("PAGE", "stop success=$success")
                setStatus(if (success) "✅ 加载完成" else "❌ 加载失败")
            }

            override fun onSecurityChange(
                s: GeckoSession,
                info: GeckoSession.ProgressDelegate.SecurityInformation
            ) {
                EchApp.log("TLS", "security=${info.origin} secure=${info.isSecure}")
            }
        }

        EchApp.log("GECKO", "opening session...")
        session.open(runtime)
        EchApp.log("GECKO", "session opened, setting view...")
        geckoView.setSession(session)
        setStatus("ech-doh + GeckoView 就绪")
        EchApp.log("GECKO", "view set, loading...")
        load()
    }

    private fun load() {
        val raw = address.text.toString().trim()
        val url = if (raw.startsWith("http")) raw else "https://$raw"
        EchApp.log("USER", "load=$url")
        try {
            session.loadUri(url)
            EchApp.log("USER", "loadUri called OK")
        } catch (e: Throwable) {
            EchApp.log("USER", "loadUri FAILED: $e")
        }
    }

    private fun setStatus(s: String) {
        runOnUiThread { status.text = s }
    }

    private fun showLogs() {
        // 从公共 Download 目录读日志
        var text = "暂无日志"
        try {
            if (Build.VERSION.SDK_INT >= 29) {
                val resolver = contentResolver
                resolver.query(
                    MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                    arrayOf(MediaStore.Downloads.DISPLAY_NAME, MediaStore.Downloads.DATA),
                    "${MediaStore.Downloads.DISPLAY_NAME} = ?",
                    arrayOf("echbrowser.log"), null
                )?.use { c ->
                    if (c.moveToFirst()) {
                        val data = c.getString(c.getColumnIndexOrThrow(MediaStore.Downloads.DATA))
                        text = File(data).readText()
                    }
                }
            } else {
                val f = File(
                    Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS),
                    "echbrowser.log"
                )
                if (f.exists()) text = f.readText()
            }
        } catch (e: Throwable) {
            text = "读取日志失败: $e\n\n${EchApp.logcatFallback()}"
        }
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
        // 从公共 Download 目录取日志文件
        val logFile = try {
            if (Build.VERSION.SDK_INT >= 29) {
                val resolver = contentResolver
                var found: File? = null
                resolver.query(
                    MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                    arrayOf(MediaStore.Downloads.DISPLAY_NAME, MediaStore.Downloads.DATA),
                    "${MediaStore.Downloads.DISPLAY_NAME} = ?",
                    arrayOf("echbrowser.log"), null
                )?.use { c ->
                    if (c.moveToFirst()) {
                        val data = c.getString(c.getColumnIndexOrThrow(MediaStore.Downloads.DATA))
                        found = File(data)
                    }
                }
                found
            } else {
                val f = File(
                    Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS),
                    "echbrowser.log"
                )
                if (f.exists()) f else null
            }
        } catch (_: Throwable) { null }

        val logFile2 = logFile
        if (logFile2 == null || !logFile2.exists()) {
            Toast.makeText(this, "暂无日志", Toast.LENGTH_SHORT).show()
            return
        }
        try {
            val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", logFile2)
            val i = Intent(Intent.ACTION_SEND).apply {
                type = "text/plain"
                putExtra(Intent.EXTRA_STREAM, uri)
                clipData = ClipData.newRawUri("日志", uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(i, "导出 ECH 浏览器日志"))
        } catch (e: Throwable) {
            Toast.makeText(this, "导出失败: ${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    override fun onDestroy() {
        EchApp.log("APP", "onDestroy")
        try {
            session.close()
            com.anglesgirl.echbrowser.echdoh.Echdoh.stop()
        } catch (_: Throwable) {}
        super.onDestroy()
    }
}
