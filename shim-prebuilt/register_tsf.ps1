# TypeAnything TSF IME 完整注册脚本
# 用法：右键 → 以管理员身份运行 PowerShell，然后：
#   cd D:\UGit\TypeAnything\shim-prebuilt
#   .\register_tsf.ps1

param(
    [string]$Action = "register"   # or "unregister"
)

$ErrorActionPreference = "Stop"

$CLSID_TIP = "{E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}"
$CLSID_PROFILE = "{E5B3C8A2-2D4F-4A6B-9C3D-7E8F1A2B4C6D}"
$GUID_TFCAT_TIP_KEYBOARD = "{34745C63-B2F0-4784-8B7D-E849F0C2E5B7}"
$LANGID = 0x0804  # zh-CN

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DllPath = Join-Path $ScriptDir "WeaselTSFShim.dll"

if (-not (Test-Path $DllPath)) {
    Write-Error "找不到 $DllPath"
    exit 1
}

function Set-RegValue {
    param($Path, $Name, $Value, $Type = "String")
    if (-not (Test-Path "Registry::$Path")) {
        New-Item -Path "Registry::$Path" -Force | Out-Null
    }
    Set-ItemProperty -Path "Registry::$Path" -Name $Name -Value $Value -Type $Type
}

if ($Action -eq "unregister") {
    Write-Host "正在注销 TypeAnything TSF 输入法..."
    Remove-Item -Path "Registry::HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\CTF\TIP\$CLSID_TIP" -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -Path "Registry::HKEY_CLASSES_ROOT\CLSID\$CLSID_TIP" -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "完成。"
    exit 0
}

Write-Host "注册 TypeAnything TSF 输入法..."
Write-Host "DLL: $DllPath"
Write-Host ""

# === 1. CLSID 注册 (HKCR) ===
$clsidRoot = "HKEY_CLASSES_ROOT\CLSID\$CLSID_TIP"
Set-RegValue -Path $clsidRoot -Name "(Default)" -Value "TypeAnything"
Set-RegValue -Path "$clsidRoot\InprocServer32" -Name "(Default)" -Value $DllPath
Set-RegValue -Path "$clsidRoot\InprocServer32" -Name "ThreadingModel" -Value "Apartment"

Write-Host "[OK] HKCR\CLSID\$CLSID_TIP"
Write-Host "[OK]     InprocServer32 = $DllPath"

# === 2. TSF TIP 注册 (HKLM) ===
$tipRoot = "HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\CTF\TIP\$CLSID_TIP"
Set-RegValue -Path $tipRoot -Name "(Default)" -Value "TypeAnything"
Set-RegValue -Path $tipRoot -Name "IconIndex" -Value 0 -Type DWord
Set-RegValue -Path $tipRoot -Name "Enable" -Value 1 -Type DWord

Write-Host "[OK] HKLM\SOFTWARE\Microsoft\CTF\TIP\$CLSID_TIP"

# === 3. LanguageProfile ===
$profileRoot = "$tipRoot\LanguageProfile"
Set-RegValue -Path $profileRoot -Name "(Default)" -Value $CLSID_PROFILE

$profileKey = "$profileRoot\$CLSID_PROFILE"
Set-RegValue -Path $profileKey -Name "(Default)" -Value "TypeAnything"
Set-RegValue -Path $profileKey -Name "Description" -Value "TypeAnything - 五笔输入+AI翻译"
Set-RegValue -Path $profileKey -Name "Reserved" -Value 0 -Type DWord

Write-Host "[OK] LanguageProfile\$CLSID_PROFILE"

# === 4. Category 注册 (TSF keyboard 类别) ===
Set-RegValue -Path "$tipRoot\Category\Category\$GUID_TFCAT_TIP_KEYBOARD" -Name "(Default)" -Value ""
Set-RegValue -Path "$tipRoot\Category\Item\$GUID_TFCAT_TIP_KEYBOARD\$($LANGID.ToString('X8'))" -Name "(Default)" -Value ""

Write-Host "[OK] Category\$GUID_TFCAT_TIP_KEYBOARD"

# === 5. Profile description (HKCU) ===
$cuProfile = "HKEY_CURRENT_USER\SOFTWARE\Microsoft\CTF\TIP\$CLSID_TIP\LanguageProfile\$CLSID_PROFILE\DisplayDescription"
Set-RegValue -Path $cuProfile -Name "(Default)" -Value "TypeAnything - 五笔输入+AI翻译"

Write-Host "[OK] HKCU DisplayDescription"
Write-Host ""
Write-Host "✅ 注册完成！"
Write-Host ""
Write-Host "下一步："
Write-Host "  1. 重启 Windows（或登出再登入）让 TSF 重新加载注册表"
Write-Host "  2. 设置 → 时间和语言 → 语言 → 中文（中华人民共和国）→ 语言选项"
Write-Host "  3. 添加键盘 → 选择 TypeAnything"
Write-Host "  4. 启动 daemon: 双击 start_daemon.bat"
