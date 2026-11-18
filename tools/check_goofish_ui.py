import base64, json, os, sys, time, urllib.request
import websocket

PORT = 18199
BASE = 'http://127.0.0.1:9222'
TEMP = os.environ.get('TEMP', '/tmp')

W = int(os.environ.get('W', '1280'))
H = int(os.environ.get('H', '900'))
TAG = os.environ.get('TAG', 'd')

with urllib.request.urlopen(BASE + '/json/list', timeout=8) as f:
    tabs = json.load(f)
tab = [t for t in tabs if t.get('type') == 'page'][0]
ws = websocket.create_connection(tab['webSocketDebuggerUrl'], timeout=180, suppress_origin=True)
ID = [0]


def cd(method, params=None):
    ID[0] += 1
    ws.send(json.dumps({'id': ID[0], 'method': method, 'params': params or {}}))
    while True:
        r = json.loads(ws.recv())
        if r.get('id') == ID[0]:
            return r


def ev(expr):
    r = cd('Runtime.evaluate', {'expression': expr, 'returnByValue': True, 'awaitPromise': True})
    return (r.get('result') or {}).get('result', {}).get('value')


def shot(name):
    d = cd('Page.captureScreenshot', {'format': 'png'})
    p = os.path.join(TEMP, name)
    open(p, 'wb').write(base64.b64decode((d.get('result') or {})['data']))
    return p


cd('Page.enable')
cd('Emulation.setDeviceMetricsOverride',
   {'width': W, 'height': H, 'deviceScaleFactor': 1, 'mobile': W < 700})
cd('Emulation.setEmulatedMedia',
   {'features': [{'name': 'prefers-reduced-motion', 'value': 'no-preference'}]})
cd('Page.navigate', {'url': 'http://127.0.0.1:%d/' % PORT})
time.sleep(4)

# 过店铺闸门（若出现）
print('gate:', ev("""(() => {
  const norm = s => (s||'').replace(/\\s+/g,' ').trim();
  const el = [...document.querySelectorAll('button,div,li')]
    .find(e => norm(e.innerText) === '鱼小铺' && e.getBoundingClientRect().width);
  if (!el) return 'nogate';
  el.click(); return 'ok';
})()"""))
time.sleep(3)

print('nav:', ev("""(() => {
  const norm = s => (s||'').replace(/\\s+/g,' ').trim();
  const el = [...document.querySelectorAll('nav button, .nav button, header button, .tab, .nav-item')]
    .find(e => norm(e.innerText) === '设置');
  if (!el) return 'nonav';
  el.click(); return 'ok';
})()"""))
time.sleep(3)

print('open:', ev("""(() => {
  const norm = s => (s||'').replace(/\\s+/g,' ').trim();
  const el = [...document.querySelectorAll('.settings-entry')]
    .find(e => norm(e.innerText).startsWith('闲鱼同步'));
  if (!el) return 'notfound';
  el.click(); return 'ok';
})()"""))
time.sleep(2)

if os.environ.get('SYNC') == '1':
    print('sync:', ev("""(() => {
      const b = [...document.querySelectorAll('.gf-modal button')].find(x => x.innerText.includes('立即同步'));
      if (!b) return 'no';
      b.click(); return 'ok';
    })()"""))
    for i in range(25):
        time.sleep(2)
        st = ev("""(() => document.querySelector('.gf-loading') ? 'loading'
          : (document.querySelector('.gf-table') ? 'review' : 'other'))()""")
        if st != 'loading':
            print('step:', st, 'after', (i + 1) * 2, 's')
            break
    time.sleep(1)

print('shot:', shot('gf_%s_final.png' % TAG))
info = ev("""(() => {
  const m = document.querySelector('.gf-modal');
  if (!m) return null;
  const r = m.getBoundingClientRect();
  const tbl = document.querySelector('.gf-table');
  const wrap = document.querySelector('.gf-table-wrap');
  return {
    modal: [Math.round(r.width), Math.round(r.height)],
    viewport: [innerWidth, innerHeight],
    tableScroll: tbl && wrap ? [tbl.scrollWidth, wrap.clientWidth] : null,
    hOverflow: tbl && wrap ? tbl.scrollWidth > wrap.clientWidth + 1 : null,
    chips: [...document.querySelectorAll('.gf-chip')].map(x => x.innerText),
    rows: document.querySelectorAll('.gf-table tbody tr').length
  };
})()""")
print('info:', json.dumps(info, ensure_ascii=False))
ws.close()
