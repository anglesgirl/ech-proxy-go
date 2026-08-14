package com.anglesgirl.xprobe

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Environment
import android.widget.Button
import android.widget.EditText
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.core.content.FileProvider
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : ComponentActivity() {

    private val scope = CoroutineScope(Dispatchers.Main)
    private var probing = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // 简单竖向布局，避免额外依赖
        val root = ScrollView(this)
        val content = android.widget.LinearLayout(this).apply {
            orientation = android.widget.LinearLayout.VERTICAL
            setPadding(32, 32, 32, 32)
        }
        root.addView(content)

        val title = TextView(this).apply {
            text = "x.com 强制 ECH 测试浏览器"
            textSize = 20f
        }
        content.addView(title)

        val dohLabel = TextView(this).apply { text = "DoH 端点" }
        content.addView(dohLabel)
        val dohInput = EditText(this).apply {
            setText("https://pieqllv9i7.cloudflare-gateway.com/dns-query")
            maxLines = 1
        }
        content.addView(dohInput)

        val hostsLabel = TextView(this).apply { text = "测试域名（逗号分隔）" }
        content.addView(hostsLabel)
        val hostsInput = EditText(this).apply {
            setText("x.com,www.x.com,api.x.com,video.twimg.com,abs.twimg.com,pbs.twimg.com")
            maxLines = 1
        }
        content.addView(hostsInput)

        val runBtn = Button(this).apply { text = "开始测试（强制 ECH，无降级）" }
        content.addView(runBtn)

        val result = TextView(this).apply {
            text = "点按钮开始。\n\n说明：\n- 全部域名强制 DoH 解析（无视系统 DNS）\n- 强制灌入 CF 公共 ECH 公钥，ECH 失败不降级明文\n- 自动并入 DoH 端点 IP 兜底（目标边缘 IP 被封时）\n\n日志会实时滚动显示，每测完一个域名立即出结果。"
            textSize = 14f
            setTextIsSelectable(true)
            typeface = android.graphics.Typeface.MONOSPACE
        }
        content.addView(result)

        val exportBtn = Button(this).apply { text = "导出结果 txt" }
        content.addView(exportBtn)

        setContentView(root)

        runBtn.setOnClickListener {
            if (probing) {
                Toast.makeText(this, "测试进行中...", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            val doh = dohInput.text.toString().trim()
            val hosts = hostsInput.text.toString().trim()
            if (hosts.isEmpty()) {
                Toast.makeText(this, "请输入域名", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            result.text = ""
            runBtn.isEnabled = false
            runBtn.text = "测试中..."
            probing = true
            scope.launch { runProbe(doh, hosts, runBtn) }
        }

        exportBtn.setOnClickListener {
            val text = lastResult ?: result.text.toString()
            if (text.isEmpty()) {
                Toast.makeText(this, "还没有结果", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            try {
                val dir = getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)
                    ?: filesDir
                dir.mkdirs()
                val ts = SimpleDateFormat("yyyyMMdd_HHmmss", Locale.US).format(Date())
                val file = File(dir, "xprobe_$ts.txt")
                file.writeText(text)

                // 用 FileProvider 分享，用户可直接发给 AI 分析
                val uri = FileProvider.getUriForFile(
                    this, "$packageName.fileprovider", file
                )
                val share = Intent(Intent.ACTION_SEND).apply {
                    type = "text/plain"
                    putExtra(Intent.EXTRA_STREAM, uri)
                    addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    putExtra(Intent.EXTRA_TEXT, text)
                }
                startActivity(Intent.createChooser(share, "导出测试结果"))
                Toast.makeText(this, "已导出: ${file.absolutePath}", Toast.LENGTH_LONG).show()
            } catch (e: Throwable) {
                Toast.makeText(this, "导出失败: ${e.message}", Toast.LENGTH_LONG).show()
            }
        }
    }

    private suspend fun runProbe(doh: String, hosts: String, runBtn: Button) {
        // 1. 后台启动测试（Go 侧 goroutine，立即返回）
        val startErr = withContext(Dispatchers.IO) {
            try {
                com.anglesgirl.xprobe.golib.echproxy.Echproxy.startProbe(doh, hosts)
                null
            } catch (e: Throwable) {
                e
            }
        }
        if (startErr != null) {
            result.append("启动失败: ${startErr.message}\n")
            finishProbe(runBtn)
            return
        }

        // 2. 轮询增量日志，实时滚动显示
        val sb = StringBuilder()
        while (true) {
            val done = withContext(Dispatchers.IO) {
                com.anglesgirl.xprobe.golib.echproxy.Echproxy.isProbeDone()
            }
            val delta = withContext(Dispatchers.IO) {
                com.anglesgirl.xprobe.golib.echproxy.Echproxy.pollLogs()
            }
            if (delta.isNotEmpty()) {
                sb.append(delta)
                result.text = sb.toString()
                // 自动滚到底部
                (result.parent as? ScrollView)?.post { it.fullScroll(ScrollView.FOCUS_DOWN) }
            }
            if (done) {
                // 取最终完整报告供导出
                lastResult = withContext(Dispatchers.IO) {
                    com.anglesgirl.xprobe.golib.echproxy.Echproxy.lastProbeResult()
                }
                break
            }
            delay(300)
        }
        finishProbe(runBtn)
    }

    private fun finishProbe(runBtn: Button) {
        probing = false
        runBtn.isEnabled = true
        runBtn.text = "开始测试（强制 ECH，无降级）"
    }

    private var lastResult: String? = null
}
