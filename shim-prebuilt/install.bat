@echo off
chcp 65001 >nul
title TypeAnything — 五笔输入+AI翻译 安装助手

echo ═══════════════════════════════════════════
echo  TypeAnything — 五笔输入 + AI 翻译
echo  基于新架构：TSF shim (mingw) + Go daemon
echo ═══════════════════════════════════════════
echo.

:: 获取脚本所在目录
set SHIM_DIR=%~dp0
set SHIM_DIR=%SHIM_DIR:~0,-1%

:: 检查文件
if not exist "%SHIM_DIR%\WeaselTSFShim.dll" (
    echo [错误] 找不到 WeaselTSFShim.dll
    echo        请确保脚本在 shim-prebuilt\ 目录中运行。
    pause
    exit /b 1
)
if not exist "%SHIM_DIR%\ta-daemon.exe" (
    echo [警告] 找不到 ta-daemon.exe
    echo        Go daemon 未就绪，TSF shim 将无法连接到 daemon。
    echo.
)

:menu
echo 请选择操作：
echo.
echo   [1] 注册 TSF 输入法 (DllRegisterServer)
echo   [2] 注销 TSF 输入法 (DllUnregisterServer)
echo   [3] 启动 Go daemon (后台运行)
echo   [4] 停止 Go daemon
echo   [5] 查看 Go daemon 状态
echo   [0] 退出
echo.

set /p choice="输入数字后回车: "

if "%choice%"=="1" goto register
if "%choice%"=="2" goto unregister
if "%choice%"=="3" goto start_daemon
if "%choice%"=="4" goto stop_daemon
if "%choice%"=="5" goto status
if "%choice%"=="0" goto end
goto menu

:register
echo.
echo [1/2] 注册 COM 组件...
regsvr32 /s "%SHIM_DIR%\WeaselTSFShim.dll"
if %errorlevel% neq 0 (
    echo [错误] regsvr32 失败 (错误码: %errorlevel%)
    echo        请尝试以管理员身份运行此脚本。
) else (
    echo [成功] COM 组件注册完成
)
echo.
echo [2/2] TSF 输入法已注册。
echo.
echo 下一步：
echo   - 在 Windows 语言栏中添加 "TypeAnything" 输入法
echo   - 启动 Go daemon (本菜单第3项)
echo   - 切换输入法开始使用
echo.
pause
goto menu

:unregister
echo.
echo 注销 COM 组件...
regsvr32 /u /s "%SHIM_DIR%\WeaselTSFShim.dll"
if %errorlevel% neq 0 (
    echo [错误] 注销失败 (错误码: %errorlevel%)
) else (
    echo [成功] TSF 输入法已注销
)
pause
goto menu

:start_daemon
echo.
echo 启动 Go daemon...
:: 检查是否已在运行
tasklist /FI "IMAGENAME eq ta-daemon.exe" 2>nul | find /I "ta-daemon" >nul
if %errorlevel% equ 0 (
    echo [提示] ta-daemon.exe 已在运行
) else (
    :: 以后台方式启动 (无控制台窗口)
    start /B "" "%SHIM_DIR%\ta-daemon.exe" -pipe "\\.\pipe\typeanything-ime" -rime "%APPDATA%\Rime" -dict "%SHIM_DIR%\..\go\..\third_party\weasel\librime\plugins\typeanything\schema\wubi86.dict.yaml"
    if %errorlevel% equ 0 (
        echo [成功] ta-daemon 已启动
    ) else (
        echo [错误] 启动失败
    )
)
pause
goto menu

:stop_daemon
echo.
taskkill /F /IM ta-daemon.exe 2>nul
if %errorlevel% equ 0 (
    echo [成功] ta-daemon 已停止
) else (
    echo [提示] ta-daemon 未在运行
)
pause
goto menu

:status
echo.
echo === 进程状态 ===
tasklist /FI "IMAGENAME eq ta-daemon.exe" 2>nul | findstr /I "ta-daemon"
if %errorlevel% neq 0 (
    echo ta-daemon: 未运行
)
echo.
echo === 注册表 TSF 状态 ===
reg query "HKEY_CLASSES_ROOT\CLSID\{E5B3C8A1-2D4F-4A6B-9C3D-7E8F1A2B4C6D}" 2>nul >nul
if %errorlevel% equ 0 (
    echo TSF 输入法: 已注册
) else (
    echo TSF 输入法: 未注册
)
pause
goto menu

:end
echo 再见！
