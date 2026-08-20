package com.anglesgirl.ech

/**
 * 可选诊断回调。接入方实现后注入 [Ech.diagnostics]，
 * 不实现则所有 event 调用空转（无崩溃、无日志泄露）。
 */
interface EchDiagnostics {
    /** 记录一条 ECH/HTTP 事件。throwable 可选，传入表示伴随异常。 */
    fun event(tag: String, message: String, throwable: Throwable? = null)

    /** 返回人类可读的当前状态（替代原 diagnostics() 文本）。 */
    fun status(): String = ""
}
