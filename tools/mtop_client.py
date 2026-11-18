import json, time, hashlib, urllib.parse, urllib.request, os

TEMP = r'C:/Users/Administrator/AppData/Local/Temp'
COOKIES = json.load(open(os.path.join(TEMP, 'gf_cookies.json'), encoding='utf-8'))
APPKEY = '34839810'
HOST = 'https://h5api.m.goofish.com/h5/%s/1.0/'
UA = ('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 '
      '(KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36 Edg/157.0.0.0')

def token():
    raw = COOKIES.get('_m_h5_tk', '')
    return raw.split('_')[0] if raw else ''

def call(api, data, timeout=20):
    t = str(int(time.time() * 1000))
    tok = token()
    d = json.dumps(data, ensure_ascii=False, separators=(',', ':'))
    sign = hashlib.md5(('%s&%s&%s&%s' % (tok, t, APPKEY, d)).encode('utf-8')).hexdigest()
    q = {
        'jsv': '2.7.2', 'appKey': APPKEY, 't': t, 'sign': sign,
        'api': api, 'v': '1.0', 'type': 'originaljson', 'dataType': 'json',
        'timeout': str(timeout * 1000), 'data': d
    }
    url = (HOST % api) + '?' + urllib.parse.urlencode(q)
    ck = '; '.join('%s=%s' % (k, v) for k, v in COOKIES.items())
    req = urllib.request.Request(url, headers={
        'User-Agent': UA,
        'Cookie': ck,
        'Referer': 'https://www.goofish.com/',
        'Origin': 'https://www.goofish.com',
        'Accept': 'application/json',
    })
    with urllib.request.urlopen(req, timeout=timeout) as f:
        return json.loads(f.read().decode('utf-8'))

if __name__ == '__main__':
    print('token:', token()[:12], '...')
    for api, data in [
        ('mtop.idle.web.trade.bought.list', {'pageSize': 10, 'pageNum': 1}),
        ('mtop.idle.web.trade.sold.list', {'pageSize': 10, 'pageNum': 1}),
    ]:
        try:
            r = call(api, data)
            ret = r.get('ret', [])
            dd = r.get('data') or {}
            items = dd.get('items') or []
            print('\n%s ->' % api, ret)
            print('  条数:', len(items), '| nextPage:', dd.get('nextPage'), '| total:', dd.get('totalCount'))
            if items:
                it = items[0]
                cd = it.get('commonData') or {}
                c = ((it.get('content') or {}).get('data') or {})
                h = ((it.get('head') or {}).get('data') or {})
                print('  样本:', cd.get('orderIdStr'), '|',
                      ((c.get('detailInfo') or {}).get('auctionTitle') or '')[:24], '|',
                      (c.get('priceInfo') or {}).get('price'), '|',
                      h.get('createTime'), '|', (h.get('userInfo') or {}).get('userNick'),
                      '|', h.get('statusViewMsg'))
        except Exception as e:
            print('\n%s 失败:' % api, repr(e)[:200])
