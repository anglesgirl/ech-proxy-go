# ============================================================
#  ECH Proxy - 取消 Windows 系统代理
#  以管理员身份运行 PowerShell，执行此脚本
# ============================================================

Write-Host "正在取消 Windows 系统代理..." -ForegroundColor Cyan

$regPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
Set-ItemProperty -Path $regPath -Name ProxyEnable -Value 0

# 通知系统刷新
$signature = @"
[DllImport("wininet.dll", SetLastError=true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
"@
$type = Add-Type -MemberDefinition $signature -Name "WinINet" -Namespace "PInvoke" -PassThru
$type::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
$type::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)

Write-Host "系统代理已取消" -ForegroundColor Green
