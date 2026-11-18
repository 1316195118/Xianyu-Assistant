import json, sys, time
sys.path.insert(0, r'C:/Users/Administrator/WorkBuddy AI/咸鱼记录/tools')
from mtop_client import call

MONTHS = ['2026-04', '2026-05', '2026-06', '2026-07', '2026-08', '2026-09', '2026-10', '2026-11', '2026-12']

def fetch_all(frm, to):
    out, page = [], 1
    while page <= 40:
        r = call('mtop.idle.finance.billdetail.income.query', {
            'request': {'billCode': 'goods_income', 'bizDateFrom': frm, 'bizDateTo': to,
                        'pageNo': page, 'pageSize': 50}
        })
        d = r.get('data') or {}
        rows = ((d.get('model') or {}).get('data') or [])
        if not rows:
            break
        out.extend(rows)
        if not d.get('hasNext'):
            break
        page += 1
        time.sleep(0.35)
    return out

allrows = []
for m in MONTHS:
    y, mm = m.split('-')
    import calendar
    last = calendar.monthrange(int(y), int(mm))[1]
    rs = fetch_all('%s-%s-01' % (y, mm), '%s-%s-%02d' % (y, mm, last))
    print(m, len(rs))
    allrows.extend(rs)

json.dump(allrows, open(r'C:/Users/Administrator/WorkBuddy AI/咸鱼记录/tools/_income_raw.json', 'w', encoding='utf-8'),
          ensure_ascii=False, indent=1)

inc = {}
for r in allrows:
    oid = str(r.get('orderId') or '')
    if not oid:
        continue
    a = inc.setdefault(oid, {'actual': 0.0, 'auction': 0.0, 'date': r.get('bizDate'),
                             'item': r.get('itemDesc'), 'sub': r.get('subBizName')})
    a['actual'] += float(r.get('incomeActualOrderAmount') or 0)
    a['auction'] += float(r.get('incomeAuctionPrice') or 0)

print('\n收入订单数:', len(inc))
json.dump(inc, open(r'C:/Users/Administrator/WorkBuddy AI/咸鱼记录/tools/_income_by_order.json', 'w', encoding='utf-8'),
          ensure_ascii=False, indent=1)
tot = sum(v['actual'] for v in inc.values())
print('真实成交合计: %.2f' % tot)
for oid, v in list(sorted(inc.items(), key=lambda x: x[1]['date'] or ''))[:5]:
    print(' ', oid, v['date'], 'actual=%.2f auction=%.2f' % (v['actual'], v['auction']), (v['item'] or '')[:20])
