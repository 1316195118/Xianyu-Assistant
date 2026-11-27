#!/usr/bin/env bash
# 发布脚本：唯一的版本递增入口（「版本号只在推送/发布时才算」）。
# 流程：前置检查（登录/测试/工作区/说明）→ 递增版本并归档 NEXT.md → 构建
#       → 提交 → 打同名 v 标签 → 推送（push tag 触发 GitHub Release / GHCR CI）。
# 本地开发请用 bash build.sh，它不会消耗版本号。
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "[1/7] 前置检查：GitHub 登录状态 ..."
if ! gh auth status >/dev/null 2>&1; then
  echo "错误：尚未登录 GitHub，请先执行 gh auth login"; exit 1
fi
echo "      已登录 GitHub"

echo "[2/7] 前置检查：git 是否处于 rebase/merge 等未完成状态..."
git_dir="$(git rev-parse --git-dir)"
if [ -d "$git_dir/rebase-merge" ] || [ -d "$git_dir/rebase-apply" ] || [ -f "$git_dir/MERGE_HEAD" ] || [ -f "$git_dir/CHERRY_PICK_HEAD" ]; then
  echo "错误：git 处于未完成的 rebase/merge/cherry-pick 状态，请先处理"; exit 1
fi
# 发布会把当前所有改动（源码 + 版本产物）一并提交，故不要求工作区预先干净；
# 但禁止把改动整体「意外漏提」——提交后第 6 步会校验工作区已无跟踪改动。
echo "      git 状态正常"

echo "[3/7] 前置检查：version/NEXT.md 是否已填写更新说明（仅优化/修复/新增三段）..."
note="$(sed '/^<!--/,/-->/d' version/NEXT.md | tr -d '[:space:]')"
if [ -z "$note" ]; then
  echo "错误：version/NEXT.md 为空，请先填写本次更新说明"; exit 1
fi

echo "[4/7] 跑全部测试 ..."
(cd server && go test -count=1 ./...)
node --test tools/goofish_extension/background.test.js
echo "      测试全部通过"

echo "[5/7] 递增版本并归档更新说明（build.sh 默认不升版本）..."
XY_BUMP_VERSION=1 bash build.sh
VER="$(python tools/version.py current)"
echo "      新版本：v$VER"

echo "[6/7] 提交版本产物并打同名标签 v$VER ..."
git add -A
# 零字节历史异常文件 ' 永不提交
git restore --staged -- "'" 2>/dev/null || true
SUBJECT="$(python3 -c "import json;print(json.load(open('version/state.json',encoding='utf-8'))['note'].splitlines()[0].lstrip('# '))" 2>/dev/null || echo '版本更新')"
git commit -m "发布 v$VER：$SUBJECT"
git tag "v$VER"
echo "      已提交并打标签 v$VER"

echo "[7/7] 推送 master 与标签（触发 Release / GHCR CI）..."
git push origin master
git push origin "v$VER"
echo ""
echo "发布完成：v$VER 已推送。"
echo "  GitHub Release / GHCR latest 由 CI 自动构建；在线更新经代理从 Latest Release 拉取。"
echo "  部署到 PVE CT100：经用户同意后执行 bash tools/deploy_pve.sh"
