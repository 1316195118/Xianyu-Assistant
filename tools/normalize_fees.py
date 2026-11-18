import json
from collections import OrderedDict

TR = r'C:/Users/Administrator/WorkBuddy AI/咸鱼记录/tools/'

def dedupe(rows, keyf):
    seen, out = set(), []
    for r in rows:
        k = keyf(r)
        if k in seen:
            continue
        seen.add(k)
        out.append(r)
    return out

inc_raw = json.load(open(TR + '_income_raw.json', encoding='utf-8'))
fee_raw = json.load(open(TR + '_fees_raw.json', encoding='utf-8'))
print('收入原始 %d -> 去重 %d' % (len(inc_raw), len(dedupe(inc_raw, lambda r: str(r.get('bizId'))))))
print('费用原始 %d -> 去重 %d' % (len(fee_raw), len(dedupe(fee_raw, lambda r: str(r.get('bizId'))))))

inc_raw = dedupe(inc_raw, lambda r: str(r.get('bizId')))
fee_raw = dedupe(fee_raw, lambda r: str(r.get('bizId')))

inc = OrderedDict()
for r in inc_raw:
    oid = str(r.get('orderId') or '')
    if not oid:
        continue
    a = inc.setdefault(oid, {'actual': 0.0, 'auction': 0.0, 'date': r.get('bizDate'),
                             'item': r.get('itemDesc'), 'n': 0})
    a['actual'] += float(r.get('incomeActualOrderAmount') or 0)
    a['auction'] += float(r.get('incomeAuctionPrice') or 0)
    a['n'] += 1

fee = OrderedDict()
for r in fee_raw:
    oid = str(r.get('orderId') or '')
    if not oid:
        continue
    a = fee.setdefault(oid, {'wuyou': 0.0, 'tech': 0.0, 'basic': 0.0, 'base': 0.0,
                             'date': r.get('bizDate'), 'item': r.get('itemDesc')})
    tag = {'wuyou': 'wuyou', 'tech': 'tech', 'basic': 'basic'}[r['_tag']]
    a[tag] += float(r.get('expendDeductAmount') or 0)
    a['base'] = max(a['base'], float(r.get('expendDeductOrderAmount') or 0))

json.dump(inc, open(TR + '_income_by_order.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
json.dump(fee, open(TR + '_fees_by_order.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
print('收入订单 %d | 费用订单 %d' % (len(inc), len(fee)))
print('真实成交合计 %.2f | 费用合计 %.2f' % (
    sum(v['actual'] for v in inc.values()),
    sum(v['wuyou'] + v['tech'] + v['basic'] for v in fee.values())))
