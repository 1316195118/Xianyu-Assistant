@echo off
chcp 65001 >nul
title 安装鱼助手闲鱼登录扩展
echo.
echo   即将打开扩展目录和 Edge 扩展管理页。
echo   Edge 中操作：打开“开发人员模式” -> “加载解压缩的扩展” -> 选择：
echo   %~dp0
echo.
echo   安装完成后扩展会后台自动续期并上报，日常不需要手动点检查。
echo.
start "" explorer "%~dp0"
start "" msedge.exe "edge://extensions"
echo   安装后点击扩展图标，在快捷面板填写鱼助手地址、店铺 ID 和配对码。
echo   快捷面板会显示服务器、配对码和闲鱼登录态是否当前可用。
pause
