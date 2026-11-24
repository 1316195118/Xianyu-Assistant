import json, urllib.request, sys
from pathlib import Path

API = 'http://10.10.10.251:18083'
SHOP = '3'
DRY = '--apply' not in sys.argv
TOOLS = Path(__file__).resolve().parent

def call(method, path, body=None):
    data = json.dumps(body, ensure_ascii=False).encode('utf-8') if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method,
                                 headers={'X-Shop-Id': SHOP, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=25) as f:
            return json.loads(f.read().decode('utf-8'))
    except urllib.error.HTTPError as e:
        return {'_err': e.code, '_body': e.read().decode('utf-8', 'replace')[:300]}

plan = json.load(open(TOOLS / '_sell_plan.json', encoding='utf-8'))

ok = 0
for p in plan:
    full = not (p['has_inc'] and p['has_fee'])
    note = ('服务费已按闲鱼支出账单核对（无忧卖 %.2f / 服务费 %.2f）' % (p['fy'], p['ft'])) if not full \
        else '未结算：闲鱼 11 月账单尚未生成，到手暂未扣服务费'
    if not full and abs(p['p_new'] - p['p_old']) > 0.005:
        note += '；成交价已按实收更正（原 %.2f）' % p['p_old']

    cur = call('GET', '/api/records/%d' % p['id'])
    if '_err' in cur:
        print('GET 失败', p['id'], cur)
        continue
    body = {
        'product': cur.get('product') or '',
        'buy_price': cur.get('buy_price') or 0,
        'buy_shipping': cur.get('buy_shipping') or 0,
        'buy_ship_tag': cur.get('buy_ship_tag') or '',
        'buy_order_no': cur.get('buy_order_no') or '',
        'buy_time': cur.get('buy_time') or '',
        'buy_counterparty': cur.get('buy_counterparty') or '',
        'sell_price': p['p_new'],
        'sell_fee': p['ft'],
        'sell_wuyou_fee': p['fy'],
        'sell_net': p['n_new'],
        'sell_shipping': cur.get('sell_shipping') or 0,
        'sell_ship_tag': cur.get('sell_ship_tag') or '',
        'sell_order_no': cur.get('sell_order_no') or '',
        'sell_time': cur.get('sell_time') or '',
        'sell_counterparty': cur.get('sell_counterparty') or '',
        'note': note,
        'parent_id': cur.get('parent_id') or 0,
        'quantity': cur.get('quantity') or 1,
        'unit_cost': cur.get('unit_cost') or 0,
    }
    if DRY:
        print('[预览] %-20s 价 %.2f→%.2f 费 %.2f→%.2f 无忧 %.2f→%.2f 到手 %.2f→%.2f' % (
            p['no'], p['p_old'], p['p_new'], cur.get('sell_fee') or 0, p['ft'],
            cur.get('sell_wuyou_fee') or 0, p['fy'], cur.get('sell_net') or 0, p['n_new']))
        ok += 1
        continue
    r = call('PUT', '/api/records/%d' % p['id'], body)
    if '_err' in r:
        print('PUT 失败', p['no'], r)
    else:
        ok += 1

print(('\n[预览模式] 可更新 %d 条，加 --apply 真正写入' if DRY else '\n已更新 %d 条') % ok)
