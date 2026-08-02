# ============================================================
#  ECH Proxy - 设置 Windows 系统代理
#  以管理员身份运行 PowerShell，执行此脚本
#  将系统 HTTP/HTTPS 代理指向本地 ECH Proxy
# ============================================================

$proxyHost = "127.0.0.1"
$proxyPort = "17171"
$proxyAddr = "http://${proxyHost}:${proxyPort}"

Write-Host "正在设置 Windows 系统代理 -> $proxyAddr" -ForegroundColor Cyan

# 设置注册表中的代理配置
$regPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
Set-ItemProperty -Path $regPath -Name ProxyEnable -Value 1
Set-ItemProperty -Path $regPath -Name ProxyServer -Value "${proxyHost}:${proxyPort}"
Set-ItemProperty -Path $regPath -Name ProxyOverride -Value "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"

# 通知系统刷新代理设置
$signature = @"
[DllImport("wininet.dll", SetLastError=true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
"@
$type = Add-Type -MemberDefinition $signature -Name "WinINet" -Namespace "PInvoke" -PassThru
$type::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)  # PROXY_SETTINGS_CHANGED
$type::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)  # REFRESH

Write-Host "系统代理已设置完成" -ForegroundColor Green
Write-Host "  地址: $proxyAddr" -ForegroundColor Gray
Write-Host "  绕过: localhost, 内网地址" -ForegroundColor Gray
Write-Host ""
Write-Host "取消代理请运行: .\unset-proxy.ps1" -ForegroundColor Yellow
