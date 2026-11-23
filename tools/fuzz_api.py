#!/usr/bin/env python3
"""全功能「乱字符」暴力测试（fuzz）。

*** 只对隔离实例跑，禁止指向生产库 ***（会写入大量垃圾数据）。

覆盖 4 层：
  A 通用矩阵：全部 API 路径 × 乱字符 payload（GET 查询串 / POST JSON），
              检查 5xx、panic/堆栈/SQL 错误泄露。
  B 深度回写：核心写端点把每个 payload 灌进每个字符串字段，
              写后回读比对，抓 NUL 截断、字段丢失、编码损坏。
  C 畸形协议：非法 UTF-8 字节、孤立代理项、畸形 JSON、1MB 超长 body、
              NUL 头、超长 URL、错误 Content-Type、数字溢出。
  D 并发：多 worker 同时灌乱字符，检查不崩、DB 完整性。

用法：
  python tools/fuzz_api.py --host http://127.0.0.1:18099
  python tools/fuzz_api.py --host ... --workers 8 --json fuzz_report.json
  python tools/fuzz_api.py --host ... --quick       # 冒烟（每种只取前 6 个 payload）
"""
import argparse
import concurrent.futures
import json
import os
import re
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

# Windows 控制台默认可能是 GBK；fuzz 用例会故意包含替换字符、RTL、私用区字符，
# 打印 WARN 明细时不应因为终端编码而把测试工具自身打崩。统一用 UTF-8 输出。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8", errors="backslashreplace")
    except (AttributeError, OSError):
        pass

# ------------------------------------------------------------------ payload
P = [
    ('plain',            '正常的中文商品名 25G DAC 直通线 2026'),
    ('sql_or',           "' OR 1=1 --"),
    ('sql_drop',         '"; DROP TABLE items; --'),
    ('sql_union',        "1' UNION SELECT id,product FROM items --"),
    ('sql_comment',      '/*+*/ AND 1=1'),
    ('xss_script',       '<script>alert(1)</script>'),
    ('xss_img',          '<img src=x onerror=alert(1)>'),
    ('xss_svg',          '"><svg/onload=alert(1)>'),
    ('html_entity',      '<b>bold</b>&amp;&lt;&gt;&quot;&#39;'),
    ('control_nul',      'A\x00B\x01C\x7f'),
    ('control_ws',       'A\r\nB\tC\x0b\x0c'),
    ('emoji',            '😀🔥💯🛒'),
    ('zwj_family',       '👨‍👩‍👧‍👦'),
    ('zero_width',       'a\u200bb\u200cc\ufeffd'),
    ('rtl_override',     'abc\u202edef\u202c'),
    ('combining',        'e\u0301\u0301\u0301' * 20),
    ('fullwidth',        'Ｖｅｒｙ　Ｗｉｄｅ　全角￥１１０'),
    ('homoglyph',        'раураl.com'),
    ('template',         '{{7*7}} ${7*7} ${jndi:ldap://x}'),
    ('path_traversal',   '../../etc/passwd\\..\\windows\\system32'),
    ('special_chars',    "!@#$%^&*()_+[]{}|;':\",./<>?`~\\"),
    ('newlines',         '第一行\n第二行\r\n第三行'),
    ('long_1k',          'A' * 1000),
    ('long_10k',         '漢' * 10000),
    ('replacement',      '\ufffd\ufffd'),
    ('private_use',      '\ue000\uf8ff'),
    ('bidi_isolate',     '\u2066abc\u2069'),
    ('json_break',       '\\"}, "admin": true, "x": "'),
    ('null_literal',     'null'),
    ('percent_fmt',      '%s%s%n%n%%'),
]
NUMERIC_BAD = [-1, 1e309, -1e309, 999999999999999999999, 1.7976931348623157e308,
               0.0000001, 'NaN', 'Infinity', '-Infinity', '1e309', '0x10', '', None]

PATHS = [
    '/api/health', '/api/version', '/api/auth/status', '/api/settings',
    '/api/stats', '/api/stats/extras', '/api/trend', '/api/records',
    '/api/export', '/api/groups', '/api/groups/candidates', '/api/tags',
    '/api/purchases', '/api/backups', '/api/undo/ops', '/api/records/1',
    '/api/groups/1', '/api/groups/by-item/1', '/api/tags/1',
    '/api/records/1/attachment', '/api/backups/xxx',
    '/api/operations/health', '/api/operations/transport',
    '/api/operations/fees', '/api/operations/owners/1/statement',
    '/api/operations/lots', '/api/operations/pricing',
    '/api/operations/multi-shop',
]

# 响应体里出现这些说明后端把内部错误泄漏给了调用方
LEAK_RE = re.compile(
    r'panic:|runtime error:|goroutine \d+|SQL logic error|near "|\bSQLITE_\w+'
    r'|unexpected end of JSON input.*stack|github.com/|xianyu-stat/',
    re.I)

STR_FIELDS = ['product', 'note', 'buy_order_no', 'sell_order_no',
              'buy_counterparty', 'sell_counterparty', 'buy_ship_tag',
              'sell_ship_tag', 'buy_time', 'sell_time']

_lock = threading.Lock()
results = []          # (level, case, status, detail)
counters = {'total': 0, 'fail': 0, 'warn': 0}


def record(level, case, status, detail=''):
    with _lock:
        counters['total'] += 1
        if status == 'FAIL':
            counters['fail'] += 1
        elif status == 'WARN':
            counters['warn'] += 1
        results.append({'level': level, 'case': case, 'status': status,
                        'detail': detail[:400]})


def http(host, method, path, body=None, raw=None, headers=None, timeout=12):
    """返回 (status, text)。不抛 HTTPError，统一转成 (code, body)。"""
    url = host.rstrip('/') + path
    data = raw if raw is not None else (
        json.dumps(body, ensure_ascii=False).encode('utf-8') if body is not None else None)
    hdr = {'Content-Type': 'application/json'} if body is not None else {}
    if headers:
        hdr.update(headers)
    req = urllib.request.Request(url, data=data, headers=hdr, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode('utf-8', 'replace')
    except urllib.error.HTTPError as e:
        # Go 对超大请求头会回 431 后直接断连，e.read() 可能抛 ConnectionResetError
        try:
            body = e.read().decode('utf-8', 'replace')
        except Exception:                       # noqa: BLE001
            body = ''
        return e.code, body
    except Exception as e:                       # noqa: BLE001
        # Go net/http 对超大请求行会直接断连（等价 431），属于正确的协议层拒绝，
        # 不是应用崩溃。判为 4xx，避免误报 FAIL。
        if len(url) > 8000 and type(e).__name__ in (
                'ConnectionResetError', 'RemoteDisconnected', 'ConnectionAbortedError'):
            return 431, 'protocol-level rejection of oversized request line'
        return -1, f'{type(e).__name__}: {e}'


def judge(level, case, st, text, allow_4xx=True):
    """统一判定：网络失败/5xx/内部信息泄露 = FAIL；其余 PASS。"""
    if st < 0:
        record(level, case, 'FAIL', text)
        return
    if st >= 500:
        record(level, case, 'FAIL', f'HTTP {st}: {text[:300]}')
        return
    hit = LEAK_RE.search(text)
    if hit:
        record(level, case, 'FAIL', f'HTTP {st} 内部信息泄露: {hit.group(0)} | {text[:200]}')
        return
    if st >= 400 and not allow_4xx:
        record(level, case, 'WARN', f'HTTP {st}: {text[:200]}')
        return
    record(level, case, 'PASS', f'HTTP {st}')


def normalize_expected(value):
    """按后端 cleanControlChars 的规则计算字符串字段的期望值。"""
    return ''.join(
        ch for ch in value
        if not ((ord(ch) < 0x20 and ch not in '\t\n\r')
                or ord(ch) == 0x7f or 0x80 <= ord(ch) <= 0x9f))


def clamp_expected(value, limit):
    """按后端 clampRunes 的规则，以 Unicode 字符数截断。"""
    return value if len(value) <= limit else value[:limit]


def normalize_expected_time(value):
    """复刻后端 normalizeTime，供时间字段回读比对。

    Go 的 len/切片按 UTF-8 字节计算；遇到仅 19 字节处截断多字节字符时，
    encoding/json 会把残缺序列编码成 U+FFFD。脚本也按这一行为计算期望值，
    避免把「后端按业务规则规范化」误报成数据损坏。有效时间格式仍原样通过。
    """
    value = normalize_expected(value).strip()
    if not value:
        return ''
    value = value.replace('T', ' ', 1)
    size = len(value.encode('utf-8'))
    if size == 10:
        return value + ' 00:00:00'
    if size == 16:
        return value + ':00'
    if size > 19:
        return value.encode('utf-8')[:19].decode('utf-8', 'replace')
    return value


# ------------------------------------------------------------------ Layer A
def layer_a(host, payloads):
    for name, val in payloads:
        # GET：把乱字符塞进各种查询参数
        for p in PATHS:
            q = urllib.parse.urlencode({'q': val, 'sort': val, 'order': val,
                                       'type': val, 'page': val, 'scope': val})
            st, txt = http(host, 'GET', f'{p}?{q}')
            judge('A', f'GET {p} q={name}', st, txt)
        # POST：把乱字符塞进 JSON 各字段
        for p in ['/api/records', '/api/parse-text', '/api/match', '/api/tags',
                  '/api/groups', '/api/settings', '/api/ocr-feedback',
                  '/api/auth/login', '/api/undo', '/api/backup/verify']:
            body = {k: val for k in STR_FIELDS}
            body.update({'text': val, 'name': val, 'title': val, 'q': val,
                         'product': val, 'password': val, 'token': val,
                         'item_ids': [], 'side': 'sell'})
            st, txt = http(host, 'POST', p, body)
            judge('A', f'POST {p} {name}', st, txt)
        for p in ['/api/operations/transport/import',
                  '/api/operations/fees/reconcile',
                  '/api/operations/lots/meta']:
            body = {k: val for k in STR_FIELDS}
            body.update({'text': val, 'name': val, 'title': val, 'q': val,
                         'product': val, 'owner': val, 'lot_key': val,
                         'rows': [], 'data': {}})
            st, txt = http(host, 'POST', p, body)
            judge('A', f'POST {p} {name}', st, txt)


# ------------------------------------------------------------------ Layer B
def layer_b(host, payloads):
    for name, val in payloads:
        for field in STR_FIELDS:
            # side='' 表示「两侧一起填」，applySide 才会保留全部字段；
            # 若用 side='buy'，后端按设计清空卖侧字段，回读不一致会误报。
            body = {'product': 'fuzz基线', 'side': '', 'buy_price': 1,
                    'sell_price': 2, 'sell_net': 2, 'quantity': 1, 'no_merge': True}
            body[field] = val
            st, txt = http(host, 'POST', '/api/records', body)
            judge('B', f'create {field}={name}', st, txt)
            if st not in (200, 201):
                continue
            try:
                created = json.loads(txt).get('item') or json.loads(txt)
            except ValueError:
                record('B', f'create {field}={name}', 'FAIL', '响应不是 JSON')
                continue
            # 回读：从列表中按 id 找回来，比对字段是否被截断/损坏
            rid = created.get('id')
            if not rid:
                record('B', f'create {field}={name}', 'WARN', '响应无 id，跳过回读')
                continue
            st2, txt2 = http(host, 'GET', '/api/records?page=1&page_size=50&sort=created&order=desc')
            if st2 != 200:
                record('B', f'readback {field}={name}', 'FAIL', f'HTTP {st2}')
                continue
            try:
                items = json.loads(txt2).get('items', [])
                got = next((x for x in items if x.get('id') == rid), None)
            except ValueError:
                got = None
            if got is None:
                record('B', f'readback {field}={name}', 'WARN', '列表未找到刚写入的记录')
                continue
            # 后端会对输入做规范化：cleanControlChars 去 C0/DEL/C1，
            # product 额外 TrimSpace，时间字段还会按 normalizeTime 规范化。
            want = normalize_expected(val)
            if field == 'product':
                want = clamp_expected(want.strip(), 200)
            elif field in ('buy_time', 'sell_time'):
                want = normalize_expected_time(val)
            elif field in ('buy_order_no', 'sell_order_no',
                           'buy_counterparty', 'sell_counterparty'):
                want = clamp_expected(want, 100)
            elif field in ('buy_ship_tag', 'sell_ship_tag'):
                want = clamp_expected(want, 50)
            elif field == 'note':
                want = clamp_expected(want, 2000)
            actual = got.get(field)
            if actual != want:
                record('B', f'readback {field}={name}', 'WARN',
                       f'回读不一致 want={want!r:.60} got={actual!r:.60}')
            else:
                note = '控制字符已规范化' if want != val else f'HTTP {st2}'
                record('B', f'readback {field}={name}', 'PASS', note)

    # 数字字段溢出 / NaN / Infinity
    for i, nv in enumerate(NUMERIC_BAD):
        body = {'product': 'fuzz数字', 'side': 'sell', 'sell_price': nv,
                'sell_net': nv, 'sell_fee': nv, 'quantity': nv,
                'buy_price': nv, 'no_merge': True}
        st, txt = http(host, 'POST', '/api/records', body)
        judge('B', f'numeric[{i}]={nv!r}', st, txt)


# ------------------------------------------------------------------ Layer C
def layer_c(host):
    cases = []

    def add(name, method, path, **kw):
        cases.append((name, method, path, kw))

    add('非法 UTF-8 body', 'POST', '/api/records',
        raw=b'{"product":"\xff\xfe\x80","side":"buy"}',
        headers={'Content-Type': 'application/json'})
    add('孤立代理项 \\ud800', 'POST', '/api/records',
        raw=b'{"product":"\\ud800\\udfff","side":"buy"}',
        headers={'Content-Type': 'application/json'})
    add('畸形 JSON 截断', 'POST', '/api/records',
        raw=b'{"product": "abc', headers={'Content-Type': 'application/json'})
    add('JSON 顶层数组', 'POST', '/api/records',
        raw=b'[1,2,3]', headers={'Content-Type': 'application/json'})
    add('JSON 深层嵌套', 'POST', '/api/records',
        raw=b'{"x":' + b'[' * 200 + b'0' + b']' * 200 + b'}',
        headers={'Content-Type': 'application/json'})
    add('1MB 超长 body', 'POST', '/api/parse-text',
        raw=(b'{"text":"' + b'X' * (1024 * 1024) + b'"}'),
        headers={'Content-Type': 'application/json'})
    add('NUL 字节请求头值', 'GET', '/api/records?q=a%00b')
    add('超长 URL', 'GET', '/api/records?q=' + 'A' * 8000)
    add('错误 Content-Type', 'POST', '/api/records',
        raw=b'product=abc&side=buy', headers={'Content-Type': 'text/plain'})
    add('空 body POST', 'POST', '/api/records', raw=b'',
        headers={'Content-Type': 'application/json'})
    add('multipart 无边界', 'POST', '/api/recognize',
        raw=b'--broken\r\n\r\nxxx', headers={'Content-Type': 'multipart/form-data'})
    add('未知路由', 'GET', '/api/this-route-does-not-exist')
    add('路径穿越', 'GET', '/api/../../etc/passwd')
    add('路径 NUL', 'GET', '/api/records/%00')
    add('方法不允许', 'PATCH', '/api/records')
    add('TRACE 方法', 'TRACE', '/api/health')
    add('超长查询串值', 'GET', '/api/stats?q=' + urllib.parse.quote('漢' * 3000))
    add('重复查询参数', 'GET', '/api/records?q=a&q=b&sort=time&sort=' + 'x' * 500)
    add('CRLF 注入查询', 'GET', '/api/records?q=%0d%0aX-Injected:%20yes')

    for name, method, path, kw in cases:
        st, txt = http(host, method, path, timeout=25, **kw)
        judge('C', name, st, txt)


# ------------------------------------------------------------------ Layer D
def layer_d(host, payloads, workers, rounds):
    jobs = []
    for r in range(rounds):
        for name, val in payloads[:12]:
            jobs.append((name, val, r))
    def one(job):
        name, val, r = job
        body = {'product': f'并发{r}-{name}', 'note': val, 'side': 'buy',
                'buy_price': 1, 'buy_order_no': val, 'no_merge': True}
        st, txt = http(host, 'POST', '/api/records', body, timeout=20)
        judge('D', f'并发 {name} r{r}', st, txt)
        st2, txt2 = http(host, 'GET', '/api/stats', timeout=20)
        judge('D', f'并发查询 {name} r{r}', st2, txt2)
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as ex:
        list(ex.map(one, jobs))


def layer_e(host):
    """专项回归：CSV 公式注入 + NUL/控制字符不得入库。"""
    import csv as _csv
    import io as _io

    for marker in ['=1+1+cmd', '@SUM(1+1)', '+cmd|calc', '-1+1', '普通商品']:
        body = {'product': marker, 'note': marker, 'side': '', 'buy_price': 1,
                'sell_price': 2, 'sell_net': 2, 'quantity': 1, 'no_merge': True}
        st, txt = http(host, 'POST', '/api/records', body)
        judge('E', f'写入公式样本 {marker[:12]}', st, txt)

    for path in ['/api/export?format=csv',
                 '/api/export?format=csv&columns=product,note,buy_price',
                 '/api/export?format=profit']:
        st, txt = http(host, 'GET', path, timeout=25)
        if st != 200:
            record('E', f'CSV 导出 {path}', 'FAIL', f'HTTP {st}: {txt[:200]}')
            continue
        bad = ''
        try:
            for row in _csv.reader(_io.StringIO(txt.lstrip('\ufeff'))):
                for cell in row:
                    if cell and cell[0] in '=+-@':
                        try:
                            float(cell)
                            continue          # 合法数值（含负金额）
                        except ValueError:
                            pass
                        bad = cell[:80]
                        break
                if bad:
                    break
        except Exception as e:                # noqa: BLE001
            record('E', f'CSV 导出 {path}', 'FAIL', f'解析失败: {e}')
            continue
        if bad:
            record('E', f'CSV 导出 {path}', 'FAIL', f'裸公式单元格: {bad}')
        else:
            record('E', f'CSV 导出 {path}', 'PASS', '无裸公式单元格')

    # NUL / C1 控制字符不得写进 SQLite
    for name, val in [('NUL', 'A\x00B\x01C\x7f'), ('C1', 'x\u0080y\u009fz')]:
        st, txt = http(host, 'POST', '/api/records',
                       {'product': val, 'note': val, 'side': '', 'buy_price': 1,
                        'sell_price': 2, 'sell_net': 2, 'quantity': 1, 'no_merge': True})
        if st != 200:
            record('E', f'控制字符入库 {name}', 'FAIL', f'HTTP {st}')
            continue
        try:
            item = json.loads(txt).get('item', {})
        except ValueError:
            record('E', f'控制字符入库 {name}', 'FAIL', '响应不是 JSON')
            continue
        dirty = any(('\x00' in str(item.get(k, ''))) or ('\x7f' in str(item.get(k, '')))
                    or ('\u0080' in str(item.get(k, ''))) or ('\u009f' in str(item.get(k, '')))
                    for k in ('product', 'note'))
        record('E', f'控制字符入库 {name}', 'FAIL' if dirty else 'PASS',
               f'product={item.get("product")!r}')


def integrity(host):
    """通过 /api/health 的 db 字段确认数据库仍可 Ping；再用导出接口验证数据可读。"""
    st, txt = http(host, 'GET', '/api/health')
    ok = st == 200 and '"db":true' in txt.replace(' ', '')
    record('Z', 'DB 健康检查', 'PASS' if ok else 'FAIL', txt[:200])
    st2, txt2 = http(host, 'GET', '/api/records?page_size=1000')
    try:
        n = len(json.loads(txt2).get('items', []))
        record('Z', '导出/列表可读', 'PASS' if st2 == 200 else 'FAIL', f'{n} 条')
    except ValueError:
        record('Z', '导出/列表可读', 'FAIL', txt2[:200])


def main():
    ap = argparse.ArgumentParser(description='全功能乱字符暴力测试')
    ap.add_argument('--host', default='http://127.0.0.1:18099')
    ap.add_argument('--workers', type=int, default=8)
    ap.add_argument('--rounds', type=int, default=2)
    ap.add_argument('--quick', action='store_true')
    ap.add_argument('--json', default='')
    ap.add_argument('--only', choices=['A', 'B', 'C', 'D', 'E'], default='')
    args = ap.parse_args()

    if '10.10.10.251' in args.host:
        print('拒绝：检测到生产地址，本测试会写入大量垃圾数据，请指向隔离实例。')
        return 2
    st, _ = http(args.host, 'GET', '/api/health', timeout=5)
    if st != 200:
        print(f'目标不可达：{args.host} (HTTP {st})')
        return 2

    payloads = P[:6] if args.quick else P
    print(f'目标 {args.host} | payload {len(payloads)} 种 | 并发 {args.workers}')
    t0 = time.time()
    if not args.only or args.only == 'A':
        layer_a(args.host, payloads); print('[A] 通用矩阵完成')
    if not args.only or args.only == 'B':
        layer_b(args.host, payloads); print('[B] 深度回写完成')
    if not args.only or args.only == 'C':
        layer_c(args.host); print('[C] 畸形协议完成')
    if not args.only or args.only == 'D':
        layer_d(args.host, payloads, args.workers, args.rounds); print('[D] 并发完成')
    if not args.only or args.only == 'E':
        layer_e(args.host); print('[E] 注入专项完成')
    integrity(args.host)

    dur = time.time() - t0
    by_level = {}
    for r in results:
        d = by_level.setdefault(r['level'], {'PASS': 0, 'FAIL': 0, 'WARN': 0})
        d[r['status']] += 1
    print('\n=== 汇总 ===')
    for lv in sorted(by_level):
        d = by_level[lv]
        print(f'  {lv}: PASS {d["PASS"]:5d}  WARN {d["WARN"]:4d}  FAIL {d["FAIL"]:4d}')
    print(f'  合计 {counters["total"]} 用例 | FAIL {counters["fail"]} | WARN {counters["warn"]} | 耗时 {dur:.1f}s')
    fails = [r for r in results if r['status'] == 'FAIL']
    if fails:
        print('\n=== FAIL 明细（前 25）===')
        for r in fails[:25]:
            print(f'  [{r["level"]}] {r["case"]}: {r["detail"]}')
    warns = [r for r in results if r['status'] == 'WARN']
    if warns:
        print(f'\n=== WARN 样例（共 {len(warns)}，前 12）===')
        for r in warns[:12]:
            print(f'  [{r["level"]}] {r["case"]}: {r["detail"]}')
    if args.json:
        with open(args.json, 'w', encoding='utf-8') as f:
            json.dump({'summary': counters, 'by_level': by_level,
                       'duration': dur, 'results': results}, f, ensure_ascii=False, indent=2)
        print(f'\n详细报告：{args.json}')
    return 1 if counters['fail'] else 0


if __name__ == '__main__':
    sys.exit(main())
