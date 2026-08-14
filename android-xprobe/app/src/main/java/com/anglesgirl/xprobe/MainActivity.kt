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
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

class MainActivity : ComponentActivity() {

    private val scope = CoroutineScope(Dispatchers.Main)

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
            text = "点按钮开始。\n\n说明：\n- 全部域名强制 DoH 解析（无视系统 DNS）\n- 强制灌入 CF 公共 ECH 公钥，ECH 失败不降级明文\n- 自动并入 DoH 端点 IP 兜底（目标边缘 IP 被封时）\n\nECH 列显示 ✅accepted = ECH 握手成功\nHTTP 200 = x.com 真实可访问"
            textSize = 14f
            setTextIsSelectable(true)
            typeface = android.graphics.Typeface.MONOSPACE
        }
        content.addView(result)

        val exportBtn = Button(this).apply { text = "导出结果 txt" }
        content.addView(exportBtn)

        setContentView(root)

        runBtn.setOnClickListener {
            val doh = dohInput.text.toString().trim()
            val hosts = hostsInput.text.toString().trim()
            if (hosts.isEmpty()) {
                Toast.makeText(this, "请输入域名", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            result.text = "测试中...（每域名约 20s 超时，请耐心）"
            runBtn.isEnabled = false
            scope.launch {
                val output = withContext(Dispatchers.IO) {
                    try {
                        // gomobile 绑定：javapkg com.anglesgirl.xprobe.golib，类 Echproxy
                        com.anglesgirl.xprobe.golib.Echproxy.XProbe(doh, hosts)
                    } catch (e: Throwable) {
                        "调用失败: ${e.message}\n${e.stackTraceToString()}"
                    }
                }
                result.text = output
                runBtn.isEnabled = true
                lastResult = output
            }
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
                val uri = androidx.core.content.FileProvider.getUriForFile(
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

    private var lastResult: String? = null
}
