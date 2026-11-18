#!/usr/bin/env python3
"""多店铺改造的静态护栏：扫出没有店铺条件的 items / link_groups 查询。

背景：数据按 shop_id 逻辑隔离（单库单表），任何一条漏加条件的查询都会让
A 店读到 B 店的数据。靠人眼 review 33 处 SQL 必然出错，所以做成可执行的检查。

判定规则：
  命中 `FROM items` / `FROM link_groups` 的语句片段（含前后各 3 行、跨行拼接），
  若片段内出现 shop_id / purchaseCond（全局建索引用的常量条件）/ 白名单标记，视为安全。

用法：
  python tools/check_scope.py            # 有问题返回 1
  python tools/check_scope.py -v         # 打印每一处的判定依据
"""

import argparse
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SERVER = os.path.join(ROOT, 'server')

# 允许全表访问的位置（附理由，新增必须写清楚为什么安全）
WHITELIST = {
    ('server/db.go', 'backfillProductNorm'): '启动时回填归一化商品名，按全表幂等维护',
    ('server/purchase_index.go', 'refreshPurchaseIndex'): '一次读取全部店铺再按 shop_id 分桶建索引',
    ('server/extras.go', 'handleBackupVerify'): '备份对账统计全库行数，不对外返回明细',
    ('server/extras.go', 'countItemsInBackup'): '读的是备份文件里的临时库，与主库无关',
    ('server/undo.go', 'restoreImportReplace'): '恢复时按 shop_id 逐表清理',
    ('server/undo.go', 'restoreDeletedItems'): '按主键 id 判重，id 全局唯一必须全库判断',
    ('server/undo.go', 'restoreDeletedGroup'): '按主键 id 判重，id 全局唯一必须全库判断',
    ('server/handlers.go', 'childCount'): 'parent_id 是全局唯一主键，父单必然同店（当前无调用点）',
}

TARGET = re.compile(r'\bFROM\s+(items|link_groups)\b', re.I)
SAFE_HINT = re.compile(r'shop_id|purchaseCond|shopID|shop-scope', re.I)


def sql_windows(lines, idx, before=3, after=4):
    lo = max(0, idx - before)
    hi = min(len(lines), idx + after + 1)
    return '\n'.join(lines[lo:hi])


def enclosing_func(lines, idx):
    for i in range(idx, -1, -1):
        m = re.match(r'func\s+(?:\([^)]*\)\s*)?(\w+)', lines[i])
        if m:
            return m.group(1)
    return ''


def func_body_flags(lines, idx):
    """返回 (函数名, 该函数体内是否声明了 shop-scope)。

    标记是对整个函数的声明（例如「这里的条件来自 buildWhere」），
    而 buildWhere 的调用点往往离 FROM 语句有十几行，用固定窗口会误报。
    """
    start = 0
    name = ''
    for i in range(idx, -1, -1):
        if re.match(r'func\s+', lines[i]):
            start = i
            m = re.match(r'func\s+(?:\([^)]*\)\s*)?(\w+)', lines[i])
            name = m.group(1) if m else ''
            break
    end = len(lines)
    for j in range(start + 1, len(lines)):
        if re.match(r'func\s+', lines[j]):
            end = j
            break
    body = '\n'.join(lines[start:end])
    return name, ('shop-scope' in body)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('-v', '--verbose', action='store_true')
    args = ap.parse_args()

    problems, checked, skipped = [], 0, 0

    for name in sorted(os.listdir(SERVER)):
        if not name.endswith('.go') or name.endswith('_test.go'):
            continue
        path = os.path.join(SERVER, name)
        rel = 'server/' + name
        with open(path, encoding='utf-8') as f:
            lines = f.read().split('\n')

        for i, line in enumerate(lines):
            if not TARGET.search(line):
                continue
            checked += 1
            fn, declared = func_body_flags(lines, i)
            window = sql_windows(lines, i)

            if (rel, fn) in WHITELIST:
                skipped += 1
                if args.verbose:
                    print(f'  SKIP  {rel}:{i + 1}  {fn}()  —— {WHITELIST[(rel, fn)]}')
                continue

            if declared:
                if args.verbose:
                    print(f'  OK    {rel}:{i + 1}  {fn}()  —— 函数内已声明 shop-scope')
                continue

            if SAFE_HINT.search(window):
                if args.verbose:
                    print(f'  OK    {rel}:{i + 1}  {fn}()')
                continue

            problems.append((rel, i + 1, fn, line.strip()))

    print(f'扫描 FROM items / link_groups 共 {checked} 处：'
          f'通过 {checked - len(problems) - skipped}、白名单 {skipped}、缺条件 {len(problems)}')

    if problems:
        print('\n以下查询没有店铺条件，会跨店串数据：')
        for rel, ln, fn, code in problems:
            print(f'  {rel}:{ln}  {fn}()')
            print(f'      {code[:110]}')
        return 1

    print('全部查询已带店铺条件')
    return 0


if __name__ == '__main__':
    sys.exit(main())
