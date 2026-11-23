#!/usr/bin/env bash
# 一键构建：刷新许可证 -> 打包 Vue -> 同步到 Go 嵌入目录 -> 编译单文件二进制
set -e
ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "[1/8] 刷新第三方许可证声明（版权合规，须在 Vite 构建前）..."
cd "$ROOT"
python tools/collect_licenses.py

echo "[2/8] 打包浏览器登录同步扩展 ..."
python tools/package_goofish_extension.py

echo "[3/8] 安装前端依赖并打包 Vue ..."
cd "$ROOT/web"
npm ci --no-audit --no-fund
# 逐文件清空 dist，避免批量删除触发安全策略；Vite 只产出少量文件
python "$ROOT/tools/sync_webroot.py" prepare
npm run build

echo "[4/8] 预压缩首屏与分包资源 ..."
cd "$ROOT/server"
go run ./cmd/precompress "$ROOT/web/dist/assets"

echo "[5/8] 合并前端产物 + licenses/OCR/扩展包静态资源到 Go 嵌入目录 ..."
echo "      同时递增版本号并归档 version/NEXT.md 的更新说明（编译前请先填写）"
echo "      只想重跑同步、不升版本：XY_SKIP_VERSION=1 python tools/sync_webroot.py sync"
cd "$ROOT"
python tools/sync_webroot.py sync

echo "[6/8] 整理 Go 依赖 ..."
cd "$ROOT/server"
go mod tidy

echo "[7/8] 编译 Windows 单文件二进制 xianyu-stat.exe ..."
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$ROOT/xianyu-stat.exe" .

# 部署脚本 tools/deploy_pve.sh 上传的是 xianyu-stat-linux-amd64。
# 这一步以前靠手敲交叉编译，漏了就变成「新前端 + 旧后端」部署（改动看着没上线），
# 现在固定跑一遍，保证 build 完可以直接 deploy。
echo "[8/8] 交叉编译 Linux 二进制 xianyu-stat-linux-amd64（供 PVE 部署）..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$ROOT/xianyu-stat-linux-amd64" .

echo ""
echo "构建完成: $ROOT/xianyu-stat.exe"
echo "         $ROOT/xianyu-stat-linux-amd64"
echo "部署到 PVE: bash tools/deploy_pve.sh"
echo "本机运行: 双击 启动.bat，或 ./xianyu-stat.exe 后打开 http://localhost:8080"
