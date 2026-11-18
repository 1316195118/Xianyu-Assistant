#!/usr/bin/env python3
"""定期对账：对比线上 /api/export 的 items 数与最新 xianyu-*.db 备份的 items 表行数。

仅用 Python 标准库。备份用 sqlite3 以只读方式打开（file:...?mode=ro），不写入。
host / 备份目录 / 鉴权 Cookie 均可用命令行参数或环境变量提供：
  XY_HOST, XY_BACKUP_DIR, XY_COOKIE, XY_TIMEOUT

退出码：0=一致，1=不一致，2=出错（网络/文件/解析等，均清晰报错、不抛栈）。
"""
import argparse
import glob
import json
import os
import sqlite3
import sys
import urllib.error
import urllib.parse
import urllib.request
from urllib.parse import quote

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_HOST = "http://10.10.10.251:18083"


def api_items_count(host, cookie, timeout):
    """请求 /api/export?format=json，返回 items 数量。"""
    url = host.rstrip("/") + "/api/export?format=json"
    headers = {"Cookie": cookie} if cookie else {}
    req = urllib.request.Request(url, headers=headers, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read().decode("utf-8", "replace")
    return _count_from(json.loads(raw))


def _count_from(obj):
    """从导出 JSON 中提取 items 数，兼容若干常见结构。"""
    if isinstance(obj, list):
        return len(obj)
    if isinstance(obj, dict):
        for key in ("items", "records", "rows", "list"):
            val = obj.get(key)
            if isinstance(val, list):
                return len(val)
        for key in ("total", "count"):
            val = obj.get(key)
            if isinstance(val, int) and not isinstance(val, bool):
                return val
        # 兼容 {"data": {"items": [...]}} 这类信封
        data = obj.get("data")
        if isinstance(data, (dict, list)):
            return _count_from(data)
    raise ValueError("返回 JSON 中找不到 items 列表（已尝试 items/records/rows/list/total/count/data）")


def latest_backup(backup_dir):
    """按修改时间取最新的一份 xianyu-*.db。"""
    files = [f for f in glob.glob(os.path.join(backup_dir, "xianyu-*.db")) if os.path.isfile(f)]
    if not files:
        return None
    return max(files, key=lambda p: (os.path.getmtime(p), p))


def db_items_count(path):
    """只读打开备份，数 items 表行数。"""
    p = os.path.abspath(path).replace("\\", "/")
    uri = "file:" + quote(p, safe="/:") + "?mode=ro"
    last_err = None
    # 首选 URI 只读打开；个别平台对非 ASCII 路径的 URI 支持不佳时，
    # 退回到普通打开并立即 PRAGMA query_only=ON 保持只读语义。
    for make in (
        lambda: sqlite3.connect(uri, uri=True, timeout=5),
        lambda: sqlite3.connect(path, timeout=5),
    ):
        try:
            conn = make()
            try:
                conn.execute("PRAGMA query_only=ON")
                return int(conn.execute("SELECT COUNT(*) FROM items").fetchone()[0])
            finally:
                conn.close()
        except sqlite3.Error as e:
            last_err = e
    raise last_err


def main():
    ap = argparse.ArgumentParser(
        description="对账：线上 /api/export items 数 vs 最新 xianyu-*.db 备份 items 表行数"
    )
    ap.add_argument("--host", default=os.environ.get("XY_HOST") or DEFAULT_HOST,
                    help=f"服务地址（默认 {DEFAULT_HOST}，或环境变量 XY_HOST）")
    ap.add_argument("--backup-dir", default=os.environ.get("XY_BACKUP_DIR") or os.path.join(ROOT, "backups"),
                    help="备份目录（默认 <项目根>/backups，或环境变量 XY_BACKUP_DIR）")
    ap.add_argument("--cookie", default=os.environ.get("XY_COOKIE") or "",
                    help="鉴权 Cookie，如 xy_session=xxx（环境变量 XY_COOKIE；接口开启密码时必需）")
    ap.add_argument("--timeout", type=float, default=float(os.environ.get("XY_TIMEOUT") or "10"),
                    help="HTTP 超时秒数（默认 10）")
    args = ap.parse_args()

    # 1) 线上 items 数
    try:
        api_n = api_items_count(args.host, args.cookie, args.timeout)
    except urllib.error.HTTPError as e:
        if e.code == 401:
            print("[错误] HTTP 401：接口需要登录鉴权，请用 --cookie/XY_COOKIE 传入 xy_session Cookie", file=sys.stderr)
        elif e.code == 404:
            print("[错误] HTTP 404：/api/export 不存在（后端可能尚未提供该接口）", file=sys.stderr)
        else:
            print(f"[错误] HTTP {e.code}：访问 {args.host}/api/export 失败", file=sys.stderr)
        sys.exit(2)
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        print(f"[错误] 网络不可达：{args.host}（{e}）", file=sys.stderr)
        print("       请确认 --host/XY_HOST 是否正确、服务是否已启动。", file=sys.stderr)
        sys.exit(2)
    except ValueError as e:
        print(f"[错误] 响应解析失败：{e}", file=sys.stderr)
        sys.exit(2)

    # 2) 备份 items 数
    bak = latest_backup(args.backup_dir)
    if bak is None:
        print(f"[错误] 在备份目录未找到 xianyu-*.db：{args.backup_dir}", file=sys.stderr)
        print("       可用 --backup-dir/XY_BACKUP_DIR 指向备份所在目录", file=sys.stderr)
        print("       （例如 PVE 宿主上的 /root/xianyu-deploy-backup，或先把它拉到本地）。", file=sys.stderr)
        sys.exit(2)
    try:
        db_n = db_items_count(bak)
    except sqlite3.Error as e:
        print(f"[错误] 只读打开备份失败：{bak}\n       {e}", file=sys.stderr)
        sys.exit(2)

    # 3) 对比
    consistent = api_n == db_n
    print(f"接口: {args.host}/api/export?format=json")
    print(f"API items: {api_n}")
    print(f"备份文件: {bak}")
    print(f"备份 items: {db_n}")
    print(f"consistent: {'true' if consistent else 'false'}")
    sys.exit(0 if consistent else 1)


if __name__ == "__main__":
    main()
