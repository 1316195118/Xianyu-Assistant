import json, sys, time
from pathlib import Path
TOOLS = Path(__file__).resolve().parent
sys.path.insert(0, str(TOOLS))
from mtop_client import call

BIZ = {
    'IDLE_WYM_CATEGORY_FEE_STD_PROCESS': 'wuyou',
    'IDLE_TECHNICAL_SERVICE_FEE_STD_PROCESS': 'tech',
    'IDLE_ALL_SERVICE_FEE_STD_PROCESS': 'basic',
}

def fetch_all(biz, frm, to):
    out, page = [], 1
    while page <= 40:
        r = call('mtop.idle.finance.billdetail.expense.query', {
            'request': {'billCode': 'common_settle', 'bizDateFrom': frm, 'bizDateTo': to,
                        'pageNo': page, 'pageSize': 50, 'bizCode': biz}
        })
        d = r.get('data') or {}
        rows = ((d.get('model') or {}).get('data') or [])
        if not rows:
            break
        out.extend(rows)
        if not d.get('hasNext'):
            break
        page += 1
        time.sleep(0.4)
    return out

MONTHS = [('2026-05-01', '2026-05-31'), ('2026-06-01', '2026-06-30'), ('2026-07-01', '2026-07-31'),
          ('2026-08-01', '2026-08-31'), ('2026-09-01', '2026-09-30'),
          ('2026-10-01', '2026-10-31'), ('2026-11-01', '2026-11-30')]

rows = []
for frm, to in MONTHS:
    for biz, tag in BIZ.items():
        rs = fetch_all(biz, frm, to)
        print('%s %-38s %d' % (frm[:7], tag, len(rs)))
        for r in rs:
            r['_tag'] = tag
        rows.extend(rs)

json.dump(rows, open(TOOLS / '_fees_raw.json', 'w', encoding='utf-8'),
          ensure_ascii=False, indent=1)

agg = {}
for r in rows:
    oid = str(r.get('orderId') or r.get('primaryOrderId') or '')
    if not oid:
        continue
    a = agg.setdefault(oid, {'wuyou': 0.0, 'tech': 0.0, 'basic': 0.0, 'date': r.get('bizDate'),
                             'item': r.get('itemDesc'), 'price': r.get('itemPrice'), 'refund': 0.0})
    a[r['_tag']] += float(r.get('expendDeductAmount') or 0)
    a['refund'] += float(r.get('expendDeductRefundAmount') or 0)

print('\n订单数:', len(agg), '| 明细行:', len(rows))
tot = sum(v['wuyou'] + v['tech'] + v['basic'] for v in agg.values())
print('费用合计: %.2f' % tot)
json.dump(agg, open(TOOLS / '_fees_by_order.json', 'w', encoding='utf-8'),
          ensure_ascii=False, indent=1)
for i, (k, v) in enumerate(sorted(agg.items(), key=lambda x: x[1]['date'] or '')):
    if i >= 8:
        break
    print(' ', k, v['date'], 'wuyou=%.2f tech=%.2f basic=%.2f' % (v['wuyou'], v['tech'], v['basic']), '|', (v['item'] or '')[:20])
