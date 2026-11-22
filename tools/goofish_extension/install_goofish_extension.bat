@echo off
chcp 65001 >nul
title 安装鱼助手闲鱼登录扩展
echo.
echo   即将打开扩展目录和 Edge 扩展管理页。
echo   Edge 中操作：打开“开发人员模式” -> “加载解压缩的扩展” -> 选择：
echo   %~dp0
echo.
start "" explorer "%~dp0"
start "" msedge.exe "edge://extensions"
echo   安装后点击扩展图标，填写鱼助手地址和店铺 ID。
pause
