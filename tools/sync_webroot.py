#!/usr/bin/env python3
"""构建辅助：清理旧前端产物 / 合并静态资源到 server/webroot（供 Go embed 打包）。

Vite 只产出 index.html + assets；licenses 与 OCR WASM 等大批量静态资源放在 static/，
由此脚本合并。全程逐文件操作，不用 `rm -rf`，规避环境的安全删除策略。

用法：
  python tools/sync_webroot.py prepare   # Vite 构建前：逐文件清空 dist
  python tools/sync_webroot.py sync      # Vite 构建后：合并 dist + static -> server/webroot
"""
import os, shutil, sys, stat, hashlib, re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DIST = os.path.join(ROOT, 'web', 'dist')
STATIC = os.path.join(ROOT, 'static')
PUBLIC = os.path.join(ROOT, 'web', 'public')
DEST = os.path.join(ROOT, 'server', 'webroot')


def clear_contents(path):
    """逐文件清空目录内容（保留目录本身，规避批量删除保护）"""
    if not os.path.isdir(path):
        return 0
    n = 0
    for name in os.listdir(path):
        p = os.path.join(path, name)
        if os.path.isdir(p) and not os.path.islink(p):
            n += clear_contents(p)
            os.rmdir(p)
        else:
            os.remove(p)
            n += 1
    return n


def prepare():
    # 零删除构建：不再清理 dist。Vite 输出固定文件名（app.js/app.css），
    # 每次覆盖同名文件即可，不会产生旧 hash 堆积。
    os.makedirs(DIST, exist_ok=True)
    print('零删除构建：跳过 dist 清理')


def overwrite(src, dst):
    """二进制覆盖写（不删除目标），规避 Windows 只读属性导致的 EPERM"""
    with open(src, 'rb') as fi:
        data = fi.read()
    if os.path.exists(dst):
        os.chmod(dst, os.stat(dst).st_mode | stat.S_IWRITE)
    with open(dst, 'wb') as fo:
        fo.write(data)


def copy_tree(src_root, count, only_top=()):
    for base, dirs, files in os.walk(src_root):
        rel = os.path.relpath(base, src_root)
        if rel == '.' and only_top:
            dirs[:] = [d for d in dirs if d in only_top]
        target = DEST if rel == '.' else os.path.join(DEST, rel)
        os.makedirs(target, exist_ok=True)
        for f in files:
            if rel == '.' and only_top and f != 'index.html':
                continue
            overwrite(os.path.join(base, f), os.path.join(target, f))
            count += 1
    return count


def clean_index_html():
    """读 DEST/index.html 并去掉版本 meta，得到可稳定求哈希的内容。"""
    idx = os.path.join(DEST, 'index.html')
    if not os.path.isfile(idx):
        return None, ''
    with open(idx, 'r', encoding='utf-8') as f:
        html = f.read()
    return idx, re.sub(r'\s*<meta name="xy-(build|version)"[^>]*>', '', html)


def inject_meta(build, version=''):
    """写入 xy-build（前端内容哈希）与 xy-version（语义版本号）两个 meta。

    资源已改为内容哈希文件名，URL 本身即可 cache-bust；xy-build 仍保留供
    截图核对，xy-version 让页面无需请求就能显示版本号。只改 DEST 副本，
    不动 dist 源文件；先去掉旧 meta 再写，保证可重复执行、不叠加。
    """
    idx, html = clean_index_html()
    if not idx:
        return ''
    meta = f'    <meta name="xy-build" content="{build}" />\n'
    if version:
        meta += f'    <meta name="xy-version" content="{version}" />\n'
    html = html.replace('</head>', meta + '  </head>')
    with open(idx, 'w', encoding='utf-8') as f:
        f.write(html)
    return build


def sync():
    if not os.path.isdir(DIST):
        raise SystemExit('错误：web/dist 不存在，请先运行 npm run build')
    os.makedirs(DEST, exist_ok=True)
    # 内容哈希文件名：旧文件不会被同名覆盖，先逐文件清空目标 assets，
    # 避免 webroot 里堆积历史版本（每次部署体积只增不减）。
    assets_dest = os.path.join(DEST, 'assets')
    removed = clear_contents(assets_dest)
    count = copy_tree(DIST, 0, only_top=('assets',))  # index.html + assets
    if os.path.isdir(STATIC):
        count = copy_tree(STATIC, count)  # licenses + ocr
    if os.path.isdir(PUBLIC):
        count = copy_tree(PUBLIC, count)  # sw.js 等 public 资源

    # 每次完整构建都经过这里：递增语义版本号、归档 version/NEXT.md 的更新说明，
    # 并生成 server/version_gen.go 供后端编译进二进制。
    # 只想重跑同步、不想升版本时设 XY_SKIP_VERSION=1（例如本地调试/测试）。
    _, clean_html = clean_index_html()
    build = hashlib.md5(clean_html.encode('utf-8')).hexdigest()[:10]
    version = ''
    if os.environ.get('XY_SKIP_VERSION') != '1':
        try:
            import importlib.util
            spec = importlib.util.spec_from_file_location(
                'xy_version', os.path.join(ROOT, 'tools', 'version.py'))
            vmod = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(vmod)
            st = vmod.bump(build=build, quiet=True)
            version = st['version']
            print(f'版本号：{version}（第 {st["count"]} 次编译，说明已归档）')
        except Exception as e:  # noqa: BLE001  版本失败不应阻断构建
            print(f'  警告：版本号递增失败（不影响构建）：{e}')
    else:
        # 跳过递增时，仍要把「当前」版本号写进 meta。
        # 否则前端读不到 xy-version，设置页的版本号会显示成「—」。
        try:
            import json as _json
            with open(os.path.join(ROOT, 'version', 'state.json'), encoding='utf-8') as f:
                version = (_json.load(f) or {}).get('version', '')
        except Exception:  # noqa: BLE001
            version = ''
    inject_meta(build, version)
    print(f'前端产物同步完成：{count} 个文件（清理旧 assets {removed} 个）-> {DEST}（构建 {build} / 版本 {version or "跳过"}）')


if __name__ == '__main__':
    cmd = sys.argv[1] if len(sys.argv) > 1 else 'sync'
    if cmd == 'prepare':
        prepare()
    elif cmd == 'sync':
        sync()
    else:
        raise SystemExit('用法: sync_webroot.py prepare|sync')
