#!/usr/bin/env python3
"""分页/索引压测：对 /api/records 按不同 page/page_size 及带 q 的查询各跑 N 次。

纯标准库 urllib，无第三方依赖。输出每个场景的 p50 / p95 / 平均耗时(ms)与失败数。
耗时统计仅基于成功(HTTP 200)的请求；网络错误、超时、非 200 一律计入失败数。

用法示例见文件末尾 --help 或 README_ops.md。
"""
import argparse
import math
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_HOST = "http://10.10.10.251:18083"


def csv_ints(text):
    return [int(x.strip()) for x in text.split(",") if x.strip()]


def percentile(sorted_vals, p):
    """nearest-rank 百分位，sorted_vals 必须已升序。"""
    if not sorted_vals:
        return None
    k = math.ceil(p / 100.0 * len(sorted_vals)) - 1
    k = max(0, min(k, len(sorted_vals) - 1))
    return sorted_vals[k]


def once(url, cookie, timeout):
    """单次 GET，返回 (耗时ms, 是否成功)。"""
    headers = {"Cookie": cookie} if cookie else {}
    req = urllib.request.Request(url, headers=headers, method="GET")
    t0 = time.perf_counter()
    ok = False
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            resp.read()  # 读完整响应体，计入耗时
            ok = (resp.status == 200)
    except Exception:
        ok = False
    return (time.perf_counter() - t0) * 1000.0, ok


def build_scenarios(args):
    scenarios = []
    for ps in args.page_sizes:
        for page in args.pages:
            scenarios.append((f"page={page}&page_size={ps}", {"page": page, "page_size": ps}))
    for q in args.q:
        scenarios.append((f"q={q}&page=1&page_size=20", {"page": 1, "page_size": 20, "q": q}))
    return scenarios


def fmt(v):
    return "N/A" if v is None else f"{v:.1f}"


def main():
    ap = argparse.ArgumentParser(description="压测 /api/records 的分页与 q 查询")
    ap.add_argument("--host", default=os.environ.get("XY_HOST") or DEFAULT_HOST,
                    help=f"服务地址（默认 {DEFAULT_HOST}，或环境变量 XY_HOST）")
    ap.add_argument("-n", "--times", type=int, default=20,
                    help="每个场景重复次数（默认 20）")
    ap.add_argument("--q", action="append", default=None,
                    help="带 q 的查询词，可重复；默认 ['a']，建议传真实搜索词以命中索引")
    ap.add_argument("--page-sizes", type=csv_ints, default=[20, 50, 100],
                    help="逗号分隔的 page_size 列表（默认 20,50,100；后端上限 200）")
    ap.add_argument("--pages", type=csv_ints, default=[1, 2, 5],
                    help="逗号分隔的 page 列表（默认 1,2,5）")
    ap.add_argument("--cookie", default=os.environ.get("XY_COOKIE") or "",
                    help="鉴权 Cookie，如 xy_session=xxx（环境变量 XY_COOKIE；接口开启密码时必需）")
    ap.add_argument("--timeout", type=float, default=float(os.environ.get("XY_TIMEOUT") or "10"),
                    help="单次请求超时秒数（默认 10）")
    args = ap.parse_args()

    if args.times < 1:
        ap.error("--times/-n 必须 >= 1")
    args.q = args.q or ["a"]

    scenarios = build_scenarios(args)
    base = args.host.rstrip("/") + "/api/records"
    print(f"host={args.host}  N={args.times}  场景数={len(scenarios)}  timeout={args.timeout}s")
    print("-" * 88)

    total_ok = 0
    total_fail = 0
    for label, params in scenarios:
        url = base + "?" + urllib.parse.urlencode(params)
        latencies = []
        fails = 0
        for _ in range(args.times):
            dt, ok = once(url, args.cookie, args.timeout)
            if ok:
                latencies.append(dt)
            else:
                fails += 1
        latencies.sort()
        avg = sum(latencies) / len(latencies) if latencies else None
        p50 = percentile(latencies, 50)
        p95 = percentile(latencies, 95)
        total_ok += len(latencies)
        total_fail += fails
        print(f"{label} | N={args.times} 失败={fails} "
              f"平均={fmt(avg)}ms p50={fmt(p50)}ms p95={fmt(p95)}ms")

    print("-" * 88)
    print(f"合计: 请求={total_ok + total_fail}  成功={total_ok}  失败={total_fail}")
    if total_ok == 0:
        print("提示：全部请求失败。请确认 /api/records 是否已提供、--host 是否正确；"
              "接口开启密码时需用 --cookie/XY_COOKIE 传入 xy_session Cookie。")
    sys.exit(0)


if __name__ == "__main__":
    main()
