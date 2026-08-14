package com.anglesgirl.echbrowser

import android.app.Application
import android.content.ContentValues
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import java.io.File
import java.io.FileOutputStream
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 崩溃日志系统 —— 日志写入公共 Download 目录（MediaStore，无需权限）。
 *
 * 之前的问题：日志写在 filesDir 私有目录，Android 11+ 用户进不去
 * Android/data。改为公共 Download/echbrowser.log，文件管理器直接可见。
 *
 * 功能：
 *   1. Application 最早初始化 —— 进程一启动就建日志文件
 *   2. 全局 UncaughtExceptionHandler —— Java 崩溃立即写盘
 *   3. 同步写盘 + 双路（MediaStore 公共目录 + logcat）
 */
class EchApp : Application() {

    companion object {
        private var publicLogUri: Uri? = null
        private var appContext: Application? = null

        fun init(ctx: Application) {
            appContext = ctx
            try {
                publicLogUri = createPublicLog()
                log("APP", "public log ready: $publicLogUri")
            } catch (e: Throwable) {
                Log.e("EchBrowser", "public log init failed: $e")
            }
        }

        fun log(tag: String, msg: String) {
            val line = "${ts()} [$tag] $msg\n"
            // 1. 公共 Download 目录（MediaStore 追加写）
            appendPublic(line)
            // 2. logcat（adb logcat -s EchBrowser 可抓）
            Log.i("EchBrowser", line.trim())
        }

        fun crash(thread: Thread, t: Throwable) {
            val sb = StringBuilder()
            sb.append("${ts()} [CRASH] thread=${thread.name}\n")
            sb.append("${ts()} [CRASH] ${t}\n")
            for (el in t.stackTrace) {
                sb.append("${ts()} [CRASH]   at $el\n")
            }
            t.cause?.let { c ->
                sb.append("${ts()} [CRASH] Caused by: $c\n")
                for (el in c.stackTrace) {
                    sb.append("${ts()} [CRASH]   at $el\n")
                }
            }
            appendPublic(sb.toString())
            Log.e("EchBrowser", sb.toString())
        }

        private fun appendPublic(text: String) {
            try {
                val uri = publicLogUri ?: return
                val ctx = appContext ?: return
                ctx.contentResolver.openFileDescriptor(uri, "wa")?.use { pfd ->
                    FileOutputStream(pfd.fileDescriptor).use { fos ->
                        fos.write(text.toByteArray())
                        fos.flush()
                    }
                }
            } catch (_: Throwable) {}
        }

        /** 在公共 Download 目录创建/重建日志文件，返回其 Uri。 */
        private fun createPublicLog(): Uri? {
            val ctx = appContext ?: return null
            val resolver = ctx.contentResolver

            if (Build.VERSION.SDK_INT >= 29) {
                // Android 10+: MediaStore.Downloads，无需权限
                // 删旧建新
                resolver.delete(
                    MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                    "${MediaStore.Downloads.DISPLAY_NAME} = ?",
                    arrayOf("echbrowser.log")
                )
                val values = ContentValues().apply {
                    put(MediaStore.Downloads.DISPLAY_NAME, "echbrowser.log")
                    put(MediaStore.Downloads.MIME_TYPE, "text/plain")
                    put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
                }
                return resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            } else {
                // Android 8/9: 尝试公共目录（需 WRITE_EXTERNAL_STORAGE，可能失败）
                val dir = Environment.getExternalStoragePublicDirectory(
                    Environment.DIRECTORY_DOWNLOADS
                )
                dir.mkdirs()
                val f = File(dir, "echbrowser.log")
                f.writeText("")
                return Uri.fromFile(f)
            }
        }

        private fun ts(): String =
            SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US).format(Date())

        /** 备用：读不到公共日志时，提示用 logcat 抓取。 */
        fun logcatFallback(): String =
            "请用 adb 抓取崩溃日志:\n" +
            "  adb logcat -s EchBrowser -b crash -d\n" +
            "或 设置->系统->开发者选项->Bug 报告"
    }

    override fun onCreate() {
        super.onCreate()
        init(this)
        log("APP", "Application onCreate pid=${android.os.Process.myPid()}")
        log("APP", "filesDir=${filesDir.absolutePath}")

        val prev = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            crash(thread, throwable)
            prev?.uncaughtException(thread, throwable)
        }
        log("APP", "crash handler installed")
    }
}
