import json, urllib.request, sys
from pathlib import Path

API = 'http://10.10.10.251:18083'
TOOLS = Path(__file__).resolve().parent

def get(path, shop=3):
    req = urllib.request.Request(API + path, headers={'X-Shop-Id': str(shop)})
    with urllib.request.urlopen(req, timeout=20) as f:
        return json.loads(f.read().decode('utf-8'))

items = get('/api/records?type=sell&page_size=200&page=1')['items']
print('卖单数:', len(items))

fees = json.load(open(TOOLS / '_fees_by_order.json', encoding='utf-8'))
print('有费用明细的订单:', len(fees))

hit, miss = [], []
for it in items:
    no = str(it.get('sell_order_no') or '').strip()
    f = fees.get(no)
    rec = {
        'id': it['id'], 'order': no, 'price': it.get('sell_price'),
        'net': it.get('sell_net'), 'fee': it.get('sell_fee'), 'wuyou': it.get('sell_wuyou_fee'),
        'ship': it.get('sell_shipping'), 'time': it.get('trade_time'), 'product': it.get('product'),
        'note': it.get('note'),
    }
    if f:
        rec['f'] = f
        hit.append(rec)
    else:
        miss.append(rec)

print('\n匹配到费用: %d | 未匹配: %d' % (len(hit), len(miss)))
print('\n--- 有费用的（前 12）---')
for r in hit[:12]:
    tot = r['f']['wuyou'] + r['f']['tech'] + r['f']['basic']
    print('%s 价%-9s 存费%-7s 存无忧%-7s | 真实 无忧%.2f 技术%.2f 基础%.2f 合计%.2f -> 到手%.2f' % (
        r['order'], r['price'], r['fee'], r['wuyou'], r['f']['wuyou'], r['f']['tech'], r['f']['basic'], tot,
        (r['price'] or 0) - tot - (r['ship'] or 0)))
print('\n--- 没费用的订单 ---')
for r in miss:
    print('%s 价%-9s %s | %s' % (r['order'] or '(无单号)', r['price'], str(r['time'])[:10], (r['product'] or '')[:26]))

json.dump({'hit': hit, 'miss': miss}, open(TOOLS / '_sell_match.json', 'w', encoding='utf-8'), ensure_ascii=False, indent=1)
