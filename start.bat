@echo off
REM ============================================================
REM  ECH Proxy - Windows 便捷启动脚本
REM  双击即可运行，Ctrl+C 退出
REM ============================================================

cd /d "%~dp0"

REM --- 查找可执行文件 ---
set "EXE=ech-proxy-windows-x86_64.exe"
if not exist "%EXE%" set "EXE=ech-proxy.exe"
if not exist "%EXE%" (
    echo [错误] 未找到 ECH Proxy 可执行文件
    echo 请从 GitHub Releases 下载 ech-proxy-windows-x86_64.exe
    echo 放到此脚本同目录后重试
    pause
    exit /b 1
)

REM --- 查找配置文件 ---
set "CONFIG=config.yaml"
if not exist "%CONFIG%" (
    echo [提示] 未找到 config.yaml，将使用默认配置
    echo       监听 127.0.0.1:17171  DoH https://1.1.1.1/dns-query
    echo.
    "%EXE%"
) else (
    "%EXE%" -config "%CONFIG%"
)

pause
