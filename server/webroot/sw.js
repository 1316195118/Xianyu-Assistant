/* 闲鱼售卖统计 Service Worker
 *
 * - install : 预缓存 / 及其引用的 assets（从 index.html 解析内容哈希文件名）
 * - fetch   : 同源 GET「网络优先、失败回退缓存」；非 GET / 跨源直接放行
 * - message : CHECK_UPDATE 对比 <meta name="xy-build">，变更时回 UPDATE_AVAILABLE
 * - activate: 清理旧版本缓存
 *
 * 说明：Vite 配置了 publicDir:false，不会自动把 public/ 拷进产物。
 *       本文件需由 tools/sync_webroot.py 把 web/public/ 合并进 webroot 后才会被分发，
 *       且页面里需要注册 navigator.serviceWorker.register('/sw.js')（web/src 不在本次改动范围）。
 */
const CACHE = 'xianyu-stat-v2';
const BUILD_CACHE = 'xianyu-stat-build';
const BUILD_KEY = '/__xy-build__';
// assets 已是内容哈希文件名（assets/index-<hash>.js），无法硬编码，
// 改为从 index.html 解析实际引用后再预缓存。
const PRECACHE = ['/'];

/* ---------- 版本基线（借 Cache API 存一个极小值，避免引入 IndexedDB） ---------- */
function readMetaBuild(html) {
  if (!html) return null;
  const tag = html.match(/<meta[^>]*name=["']xy-build["'][^>]*>/i);
  if (!tag) return null;
  const m = tag[0].match(/content=["']([^"']*)["']/i);
  return m ? m[1] : null;
}

async function fetchBuild() {
  try {
    const res = await fetch('/', { cache: 'no-store' });
    if (!res.ok) return null;
    return readMetaBuild(await res.text());
  } catch (e) {
    return null;
  }
}

async function readBuild() {
  try {
    const c = await caches.open(BUILD_CACHE);
    const r = await c.match(BUILD_KEY, { ignoreSearch: true });
    return r ? await r.text() : null;
  } catch (e) {
    return null;
  }
}

async function writeBuild(build) {
  try {
    const c = await caches.open(BUILD_CACHE);
    await c.put(BUILD_KEY, new Response(build, { headers: { 'Content-Type': 'text/plain' } }));
  } catch (e) {
    /* 忽略 */
  }
}

/* ---------- install：逐个容错预缓存，避免单个资源 404 让 install 失败 ---------- */
// 解析 index.html 中引用的 /assets/* 资源并逐个预缓存（单个失败不阻断 install）
async function precacheShell(cache) {
  try {
    const res = await fetch('/', { cache: 'no-store' });
    if (!res.ok) return;
    const html = await res.text();
    try {
      await cache.put(new Request('/'), new Response(html, {
        headers: { 'Content-Type': 'text/html; charset=utf-8' }
      }));
    } catch (e) { /* ignore */ }
    const urls = new Set();
    const re = /(?:src|href)=["']([^"']*\/assets\/[^"']+)["']/g;
    let m;
    while ((m = re.exec(html))) urls.add(m[1].split('?')[0]);
    await Promise.all([...urls].map(async (u) => {
      try { await cache.add(u); } catch (e) { /* 单个失败不阻断 */ }
    }));
  } catch (e) { /* 离线安装时忽略 */ }
}

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    await precacheShell(cache);
    const build = await fetchBuild();
    if (build) await writeBuild(build);
    await self.skipWaiting();
  })());
});

/* ---------- activate：清旧缓存 ---------- */
self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const keep = new Set([CACHE, BUILD_CACHE]);
    const keys = await caches.keys();
    await Promise.all(keys.map((k) => (keep.has(k) ? Promise.resolve() : caches.delete(k))));
    if (!(await readBuild())) {
      const build = await fetchBuild();
      if (build) await writeBuild(build);
    }
    await self.clients.claim();
  })());
});

/* ---------- fetch：接口直连，哈希资源缓存优先，页面壳网络优先 ---------- */
self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return; // 非 GET 直接放行
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // 仅处理同源
  // 接口与 SSE 保持直连，避免 Service Worker 在每次查询前多加一层调度。
  if (url.pathname.indexOf('/api/') === 0) return;
  // 文件名带内容哈希，内容永远不变。重复访问直接读缓存，不再先等网络。
  if (url.pathname.indexOf('/assets/') === 0) {
    event.respondWith(cacheFirstAsset(req, url));
    return;
  }
  event.respondWith(networkFirst(req, url));
});

// 仅对静态壳/资源做运行时回写，保持离线副本新鲜；/api/* 不写入缓存。
function shouldCache(url) {
  return url.pathname === '/' || url.pathname.indexOf('/assets/') === 0;
}

async function networkFirst(req, url) {
  try {
    const res = await fetch(req);
    if (res && res.ok && res.type === 'basic' && shouldCache(url)) {
      try {
        const cache = await caches.open(CACHE);
        // 存到无查询参数的规范路径，避免 ?v=hash 变体堆积；
        // 回退查询用 ignoreSearch:true 即可命中。
        await cache.put(new Request(url.pathname), res.clone());
      } catch (e) {
        /* 缓存写入失败不影响本次网络响应 */
      }
    }
    return res;
  } catch (e) {
    const cached = await caches.match(req, { ignoreSearch: true });
    if (cached) return cached;
    if (req.mode === 'navigate') {
      const shell = await caches.match('/', { ignoreSearch: true });
      if (shell) return shell;
    }
    return new Response('离线且无可用缓存', {
      status: 503,
      statusText: 'Offline',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  }
}

async function cacheFirstAsset(req, url) {
  const cached = await caches.match(url.pathname, { ignoreSearch: true });
  if (cached) return cached;
  try {
    const res = await fetch(req);
    if (res && res.ok && res.type === 'basic') {
      try {
        const cache = await caches.open(CACHE);
        await cache.put(new Request(url.pathname), res.clone());
      } catch (e) {
        /* 缓存写入失败不影响本次响应 */
      }
    }
    return res;
  } catch (e) {
    return new Response('离线且无可用缓存', {
      status: 503,
      statusText: 'Offline',
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    });
  }
}

/* ---------- message：CHECK_UPDATE 对比 xy-build ---------- */
self.addEventListener('message', (event) => {
  const data = event.data || {};
  if (data.type !== 'CHECK_UPDATE') return;
  event.waitUntil(handleCheckUpdate(event, data));
});

function reply(event, msg) {
  const port = event.ports && event.ports[0];
  if (port) {
    port.postMessage(msg);
  } else if (event.source) {
    event.source.postMessage(msg);
  } else {
    self.clients.matchAll({ type: 'window' }).then((clients) => {
      clients.forEach((c) => c.postMessage(msg));
    });
  }
}

async function handleCheckUpdate(event, data) {
  // 基线优先取页面随消息传来的当前版本，否则用 SW 记录的部署版本
  const baseline = (typeof data.build === 'string' && data.build) || (await readBuild());
  const latest = await fetchBuild();
  if (!latest) return; // 离线或页面异常，无法判定，静默
  if (!baseline) {
    await writeBuild(latest); // 首次记录基线，不误报更新
    return;
  }
  if (latest === baseline) return; // 无更新
  await writeBuild(latest);
  reply(event, { type: 'UPDATE_AVAILABLE', build: latest });
}
