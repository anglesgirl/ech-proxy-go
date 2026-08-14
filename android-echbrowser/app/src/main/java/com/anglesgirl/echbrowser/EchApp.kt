package com.anglesgirl.echbrowser

import android.app.Application
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 崩溃日志系统。
 *
 * 闪退无日志的原因：崩溃发生在很早（native crash 或初始化早期），
 * MainActivity 的异步日志还没来得及写盘。本类：
 *   1. Application 最早初始化 —— App 进程一启动就创建日志文件
 *   2. 全局 UncaughtExceptionHandler —— Java 崩溃立即写盘
 *   3. 同步写盘 —— 每条日志立刻落盘，崩溃前的内容不丢
 */
class EchApp : Application() {

    companion object {
        lateinit var logFile: File
            private set

        fun log(tag: String, msg: String) {
            val line = "${ts()} [$tag] $msg\n"
            try {
                logFile.parentFile?.mkdirs()
                logFile.appendText(line) // 同步写盘，崩溃不丢
            } catch (_: Throwable) {}
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
            try {
                logFile.parentFile?.mkdirs()
                logFile.appendText(sb.toString())
            } catch (_: Throwable) {}
            Log.e("EchBrowser", sb.toString())
        }

        private fun ts(): String =
            SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US).format(Date())
    }

    override fun onCreate() {
        super.onCreate()
        // 进程最早入口：创建日志文件并立即写入
        logFile = File(filesDir, "echbrowser.log")
        logFile.delete()
        log("APP", "Application onCreate pid=${android.os.Process.myPid()}")
        log("APP", "filesDir=${filesDir.absolutePath}")

        // 全局崩溃捕获
        val prev = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            crash(thread, throwable)
            prev?.uncaughtException(thread, throwable)
        }
        log("APP", "crash handler installed")
    }
}
