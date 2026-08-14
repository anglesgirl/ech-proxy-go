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

/**
 * Application：公共 Download 目录日志 + 全局崩溃捕获。
 * 闪退时日志仍在 Download/echbrowser.log 可见（Android 11+ 可访问）。
 */
class EchApp : Application() {

    companion object {
        private var publicLogUri: Uri? = null
        private var appCtx: Application? = null

        fun init(ctx: Application) {
            appCtx = ctx
            try {
                publicLogUri = createPublicLog()
            } catch (_: Throwable) {}
        }

        fun log(tag: String, msg: String) {
            val line = "${ts()} [$tag] $msg\n"
            append(line)
            Log.i("EchBrowser", line.trim())
        }

        fun crash(thread: Thread, t: Throwable) {
            val sb = StringBuilder()
            sb.append("${ts()} [CRASH] thread=${thread.name} ${t}\n")
            t.stackTrace.forEach { sb.append("${ts()} [CRASH]   at $it\n") }
            t.cause?.let { c ->
                sb.append("${ts()} [CRASH] Caused by: $c\n")
                c.stackTrace.forEach { sb.append("${ts()} [CRASH]   at $it\n") }
            }
            append(sb.toString())
            Log.e("EchBrowser", sb.toString())
        }

        private fun append(text: String) {
            try {
                val uri = publicLogUri ?: return
                val ctx = appCtx ?: return
                ctx.contentResolver.openFileDescriptor(uri, "wa")?.use { pfd ->
                    FileOutputStream(pfd.fileDescriptor).use { fos ->
                        fos.write(text.toByteArray())
                        fos.flush()
                    }
                }
            } catch (_: Throwable) {}
        }

        private fun createPublicLog(): Uri? {
            val ctx = appCtx ?: return null
            val resolver = ctx.contentResolver
            return if (Build.VERSION.SDK_INT >= 29) {
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
                resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            } else {
                val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
                dir.mkdirs()
                val f = File(dir, "echbrowser.log")
                f.writeText("")
                Uri.fromFile(f)
            }
        }

        private fun ts(): String =
            java.text.SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", java.util.Locale.US)
                .format(java.util.Date())
    }

    override fun onCreate() {
        super.onCreate()
        init(this)
        log("APP", "Application onCreate pid=${android.os.Process.myPid()}")

        val prev = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            crash(thread, throwable)
            prev?.uncaughtException(thread, throwable)
        }
        log("APP", "crash handler installed")
    }
}
