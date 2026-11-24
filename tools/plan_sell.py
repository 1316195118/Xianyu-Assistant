import json, urllib.request
from pathlib import Path

API = 'http://10.10.10.251:18083'
TOOLS = Path(__file__).resolve().parent
req = urllib.request.Request(API + '/api/records?type=sell&page_size=200',
                             headers={'X-Shop-Id': '3'})
items = json.loads(urllib.request.urlopen(req).read())['items']
inc = json.load(open(TOOLS / '_income_by_order.json', encoding='utf-8'))
fee = json.load(open(TOOLS / '_fees_by_order.json', encoding='utf-8'))

plan = []
for it in sorted(items, key=lambda x: x.get('sell_time') or ''):
    no = str(it.get('sell_order_no') or '')
    i = inc.get(no)
    f = fee.get(no)
    p_old = float(it.get('sell_price') or 0)
    n_old = float(it.get('sell_net') or 0)
    ship = float(it.get('sell_shipping') or 0)

    p_new = round(i['actual'], 2) if i else p_old
    fy = round(f['wuyou'], 2) if f else 0.0
    ft = round(f['tech'] + f['basic'], 2) if f else 0.0
    n_new = round(p_new - fy - ft - ship, 2)
    plan.append({'id': it['id'], 'no': no, 'prod': it.get('product'),
                 'p_old': p_old, 'p_new': p_new, 'n_old': n_old, 'n_new': n_new,
                 'fy': fy, 'ft': ft, 'has_inc': bool(i), 'has_fee': bool(f),
                 'time': it.get('sell_time')})

print('%-20s %8s %8s %8s %8s %6s %6s  %s' % ('单号', '价旧', '价新', '到手旧', '到手新', '无忧', '服务费', '说明'))
for p in plan:
    flag = '' if (p['has_inc'] and p['has_fee']) else ('待收入' if not p['has_inc'] else '待费用')
    print('%-20s %8.2f %8.2f %8.2f %8.2f %6.2f %6.2f  %s %s' % (
        p['no'], p['p_old'], p['p_new'], p['n_old'], p['n_new'], p['fy'], p['ft'], flag,
        (p['prod'] or '')[:14]))

print('\n有收入明细: %d | 有费用明细: %d | 两者都有: %d / %d' % (
    sum(1 for p in plan if p['has_inc']), sum(1 for p in plan if p['has_fee']),
    sum(1 for p in plan if p['has_inc'] and p['has_fee']), len(plan)))
print('价格变化条数:', sum(1 for p in plan if abs(p['p_new'] - p['p_old']) > 0.005))
print('到手合计 旧 %.2f -> 新 %.2f' % (sum(p['n_old'] for p in plan), sum(p['n_new'] for p in plan)))
json.dump(plan, open(TOOLS / '_sell_plan.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
