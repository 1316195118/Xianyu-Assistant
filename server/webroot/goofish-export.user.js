// ==UserScript==
// @name         闲鱼订单导出（配合「闲鱼售卖统计」）
// @namespace    xianyu-stat
// @version      1.0.0
// @description  在你自己的浏览器里把闲鱼网页版的订单读成 JSON，粘贴进「闲鱼售卖统计」即可批量入库。不上传任何数据。
// @match        https://www.goofish.com/*
// @match        https://*.goofish.com/*
// @match        https://2.taobao.com/*
// @run-at       document-start
// @grant        none
// ==/UserScript==

/*
 * 工作方式（为什么这么设计）
 * ---------------------------------------------------------------
 * 1) 不主动发任何请求：只在你正常翻页时，旁听页面自己发出的接口返回。
 *    所以不会被当成爬虫，也不存在「翻太快被风控」的问题。
 * 2) 不依赖 CSS 选择器：闲鱼前端类名是构建哈希，改版就失效。
 *    这里直接读接口 JSON，再按字段名特征把订单挖出来，抗改版能力强得多。
 * 3) 抓到的东西只存在你自己浏览器里，点「导出」才落成文件。
 */
(function () {
  'use strict';

  const MAX_ITEMS = 800;
  const store = new Map();      // 订单号 -> 订单
  const seenUrls = [];          // 诊断用：抓到过哪些接口
  let side = guessSide();

  // ---------- 工具 ----------
  function guessSide() {
    const u = location.href.toLowerCase();
    if (/sell|sold|卖/.test(u)) return 'sell';
    return 'buy';
  }
  function num(v) {
    if (typeof v === 'number' && isFinite(v)) return v;
    if (typeof v === 'string') {
      const m = v.replace(/[^\d.]/g, '');
      const n = parseFloat(m);
      return isFinite(n) ? n : 0;
    }
    return 0;
  }
  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function normTime(v) {
    if (v == null || v === '') return '';
    if (typeof v === 'number' || /^\d{10}$|^\d{13}$/.test(String(v))) {
      let ms = Number(v);
      if (String(v).length === 10) ms *= 1000;
      const d = new Date(ms);
      if (isNaN(d.getTime())) return '';
      return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
        ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
    }
    const s = String(v).trim();
    // 2026-11-15 12:00:00 / 2026-11-15T12:00:00.000+0800 / 2026/11/15 12:00
    const m = s.match(/(\d{4})[-/](\d{1,2})[-/](\d{1,2})[ T](\d{1,2}):(\d{1,2})(?::(\d{1,2}))?/);
    if (m) return m[1] + '-' + pad(+m[2]) + '-' + pad(+m[3]) + ' ' + pad(+m[4]) + ':' + pad(+m[5]) + ':' + pad(+(m[6] || 0));
    const d2 = s.match(/^(\d{4})[-/](\d{1,2})[-/](\d{1,2})$/);
    if (d2) return d2[1] + '-' + pad(+d2[2]) + '-' + pad(+d2[3]) + ' 00:00:00';
    return '';
  }
  function pick(obj, keys) {
    for (const k of keys) {
      if (obj && obj[k] != null && obj[k] !== '') return obj[k];
    }
    return null;
  }

  const K = {
    id: ['bizOrderId', 'orderId', 'order_id', 'tradeId', 'bizId', 'id'],
    title: ['itemTitle', 'auctionTitle', 'goodsTitle', 'itemName', 'title', 'subject'],
    price: ['payAmount', 'actualFee', 'payPrice', 'price', 'totalAmount', 'amount'],
    net: ['sellerReceive', 'sellerRealReceive', 'income', 'netAmount', 'receiveAmount'],
    fee: ['serviceFee', 'platformFee', 'poundage'],
    ship: ['postFee', 'shippingFee', 'freight', 'logisticsFee', 'expressFee'],
    time: ['createTime', 'gmtCreate', 'payTime', 'orderTime', 'tradeCreateTime', 'create'],
    cp: ['buyerNick', 'sellerNick', 'peerNick', 'nick', 'userNick', 'buyerName', 'sellerName']
  };

  // ---------- 闲鱼订单列表的真实结构 ----------
  // 每条订单是「三段式」对象，字段被拆散在兄弟节点里，通用按键名找会漏：
  //   commonData.orderIdStr / commonData.seller        订单号、我是买家还是卖家
  //   content.data.detailInfo.auctionTitle             商品标题（带「标题：」前缀）
  //   content.data.priceInfo.price                     实付款 / 实收款
  //   head.data.createTime / head.data.userInfo.userNick  时间、对方昵称
  // 实测接口：mtop.idle.web.trade.bought.list
  function fromGoofishItem(o) {
    if (!o || typeof o !== 'object' || !o.commonData) return null;
    const cd = o.commonData;
    const key = String(cd.orderIdStr || cd.orderId || '');
    if (key.length < 8) return null;
    const c = (o.content && o.content.data) || {};
    const h = (o.head && o.head.data) || {};
    const title = String((c.detailInfo && c.detailInfo.auctionTitle) || '')
      .replace(/^标题[:：]\s*/, '').trim();
    const pi = c.priceInfo || {};
    return {
      order_no: key,
      product: title,
      price: num(pi.price),
      net: num(pi.sellerReceive || pi.actualReceive || 0),
      fee: num(pi.serviceFee || 0),
      shipping: num(pi.postFee || pi.freight || 0),
      time: normTime(h.createTime),
      counterparty: String((h.userInfo && h.userInfo.userNick) || '').trim(),
      side: String(cd.seller) === 'true' ? 'sell' : 'buy',
      status: String(h.statusViewMsg || '').trim(),
      status_enum: String(cd.tradeStatusEnum || '').trim()
    };
  }

  function addOrder(item) {
    if (!item || !item.order_no) return;
    if (!item.product && !(item.price > 0)) return;
    if (store.has(item.order_no)) return;
    store.set(item.order_no, item);
    if (store.size > MAX_ITEMS) store.delete(store.keys().next().value);
  }

  // ---------- 从任意 JSON 里递归挖订单 ----------
  function walk(node, depth) {
    if (!node || depth > 8) return;
    if (Array.isArray(node)) {
      for (const it of node) walk(it, depth + 1);
      return;
    }
    if (typeof node !== 'object') return;

    // 命中闲鱼三段式订单就直接收下，不再往子节点钻，避免同一单被重复解析
    const g = fromGoofishItem(node);
    if (g) { addOrder(g); return; }

    const id = pick(node, K.id);
    const title = pick(node, K.title);
    const price = pick(node, K.price);
    if (id != null && String(id).length >= 8 && (title || price)) {
      addOrder({
        order_no: String(id),
        product: String(title || '').trim(),
        price: num(price),
        net: num(pick(node, K.net)),
        fee: num(pick(node, K.fee)),
        shipping: num(pick(node, K.ship)),
        time: normTime(pick(node, K.time)),
        counterparty: String(pick(node, K.cp) || '').trim(),
        side: ''
      });
    }
    for (const k in node) {
      const v = node[k];
      if (v && typeof v === 'object') walk(v, depth + 1);
    }
  }

  function feed(url, text) {
    if (!text || text.length < 20) return;
    if (!/[{"\[]/.test(text.slice(0, 40))) return;
    if (!/order|trade|biz|item/i.test(url + text.slice(0, 200))) return;
    let data;
    try { data = JSON.parse(text); } catch (e) { return; }
    const before = store.size;
    walk(data, 0);
    if (store.size > before) {
      seenUrls.push({ url: String(url).slice(0, 160), added: store.size - before, at: Date.now() });
      if (seenUrls.length > 40) seenUrls.shift();
      render();
    }
  }

  // ---------- 旁听 fetch ----------
  const rawFetch = window.fetch;
  if (typeof rawFetch === 'function') {
    window.fetch = function (input, init) {
      const url = typeof input === 'string' ? input : ((input && input.url) || '');
      const p = rawFetch.call(this, input, init);
      try {
        p.then(function (res) {
          const u = (res && res.url) || url;
          if (!/mtop|order|trade/i.test(u)) return;
          if (!res || typeof res.clone !== 'function') return;
          res.clone().text().then(function (t) { feed(u, t) }).catch(function () { });
        }).catch(function () { });
      } catch (e) { }
      return p;
    };
  }

  // ---------- 旁听 XHR ----------
  const rawOpen = XMLHttpRequest.prototype.open;
  const rawSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function (method, url) {
    this.__xyUrl = url;
    return rawOpen.apply(this, arguments);
  };
  XMLHttpRequest.prototype.send = function () {
    this.addEventListener('load', function () {
      try {
        if (!/mtop|order|trade/i.test(String(this.__xyUrl || ''))) return;
        const t = this.responseType === 'json' ? JSON.stringify(this.response) : this.responseText;
        feed(this.__xyUrl, t);
      } catch (e) { }
    });
    return rawSend.apply(this, arguments);
  };

  // ---------- 兜底：扫描页面内嵌的 SSR 数据 ----------
  function scanEmbedded() {
    const keys = ['__INIT_DATA__', '__NEXT_DATA__', '__PRELOADED_STATE__', '__INITIAL_STATE__'];
    for (const k of keys) {
      try { if (window[k]) walk(window[k], 0) } catch (e) { }
    }
    document.querySelectorAll('script[type="application/json"]').forEach(function (s) {
      try { walk(JSON.parse(s.textContent || '{}'), 0) } catch (e) { }
    });
    render();
  }

  // ---------- 面板 ----------
  let el = null;
  function render() {
    if (!el) return;
    const n = store.size;
    el.querySelector('[data-n]').textContent = n;
    el.querySelector('[data-side]').textContent = side === 'buy' ? '我买到的' : '我卖出的';
    el.querySelector('[data-tip]').textContent = n
      ? '继续翻页会自动累加，翻完点导出'
      : '请在页面上翻到订单列表，数据会自动收集';
  }
  function buildPanel() {
    el = document.createElement('div');
    el.id = 'xy-export-panel';
    el.innerHTML =
      '<div class="xy-head"><b>闲鱼订单导出</b><button class="xy-min" title="收起">−</button></div>' +
      '<div class="xy-body">' +
      '  <div class="xy-row">已捕获 <b data-n>0</b> 条</div>' +
      '  <div class="xy-tip" data-tip></div>' +
      '  <div class="xy-row">类型：<button class="xy-side" data-side>我买到的</button></div>' +
      '  <div class="xy-actions">' +
      '    <button class="xy-btn xy-primary" data-act="export">导出 JSON</button>' +
      '    <button class="xy-btn" data-act="copy">复制</button>' +
      '  </div>' +
      '  <div class="xy-actions">' +
      '    <button class="xy-btn" data-act="scan">扫描页面数据</button>' +
      '    <button class="xy-btn" data-act="diag">诊断</button>' +
      '    <button class="xy-btn xy-quiet" data-act="clear">清空</button>' +
      '  </div>' +
      '</div>';
    const css = document.createElement('style');
    css.textContent = [
      '#xy-export-panel{position:fixed;right:16px;bottom:16px;z-index:2147483000;width:240px;',
      'background:#fff;color:#1f2329;border:1px solid #e3e6ec;border-radius:14px;',
      'box-shadow:0 10px 30px rgba(20,26,40,.18);font:13px/1.5 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif}',
      '#xy-export-panel .xy-head{display:flex;align-items:center;padding:10px 12px;border-bottom:1px solid #eef0f4}',
      '#xy-export-panel .xy-head b{font-size:13px}',
      '#xy-export-panel .xy-min{margin-left:auto;border:0;background:transparent;font-size:16px;line-height:1;cursor:pointer;color:#9aa0aa}',
      '#xy-export-panel .xy-body{padding:10px 12px 12px}',
      '#xy-export-panel.xy-collapsed .xy-body{display:none}',
      '#xy-export-panel .xy-row{display:flex;align-items:center;gap:6px;margin-bottom:6px}',
      '#xy-export-panel .xy-row b{color:#2f6bff;font-size:15px}',
      '#xy-export-panel .xy-tip{color:#9aa0aa;font-size:11px;margin-bottom:8px}',
      '#xy-export-panel .xy-side{border:1px solid #dfe2e8;background:#fafbfc;border-radius:8px;padding:2px 8px;cursor:pointer;font-size:12px}',
      '#xy-export-panel .xy-actions{display:flex;gap:6px;margin-top:8px}',
      '#xy-export-panel .xy-btn{flex:1;border:1px solid #dfe2e8;background:#fff;border-radius:9px;padding:7px 0;cursor:pointer;font-size:12px;font-weight:600;color:#3a3f47}',
      '#xy-export-panel .xy-btn:hover{border-color:#2f6bff;color:#2f6bff}',
      '#xy-export-panel .xy-primary{background:#ffcf33;border-color:#f5b400;color:#1f2329}',
      '#xy-export-panel .xy-primary:hover{background:#ffd94d;color:#1f2329}',
      '#xy-export-panel .xy-quiet{color:#9aa0aa}'
    ].join('');
    document.documentElement.appendChild(css);
    document.body.appendChild(el);

    el.querySelector('.xy-min').onclick = function () { el.classList.toggle('xy-collapsed') };
    el.querySelector('[data-side]').onclick = function () {
      side = side === 'buy' ? 'sell' : 'buy';
      render();
    };
    el.querySelectorAll('[data-act]').forEach(function (b) {
      b.onclick = function () { act(b.getAttribute('data-act')) };
    });
    render();
  }

  function payload() {
    const rows = Array.from(store.values()).map(function (r) {
      return {
        order_no: r.order_no, product: r.product, price: r.price,
        net: r.net, fee: r.fee, wuyou: 0, shipping: r.shipping,
        time: r.time, counterparty: r.counterparty, side: r.side || side,
        status: r.status || '', status_enum: r.status_enum || ''
      };
    });
    return { source: 'goofish', side: side, exported_at: new Date().toISOString(), count: rows.length, rows: rows };
  }

  function download(text) {
    const blob = new Blob([text], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'xianyu-orders-' + Date.now() + '.json';
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(a.href) }, 3000);
  }

  function act(name) {
    if (name === 'export') {
      if (!store.size) { alert('还没有捕获到订单。\n\n请确认已经打开「我买到的 / 我卖出的」列表页并往下翻一点，再试。'); return }
      download(JSON.stringify(payload(), null, 2));
      return;
    }
    if (name === 'copy') {
      if (!store.size) { alert('还没有捕获到订单'); return }
      const text = JSON.stringify(payload(), null, 2);
      const done = function () { alert('已复制 ' + store.size + ' 条，去「闲鱼售卖统计 → 批量导入 → 粘贴 JSON」'); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done).catch(function () { fallbackCopy(text, done) });
      } else fallbackCopy(text, done);
      return;
    }
    if (name === 'scan') { scanEmbedded(); alert('已扫描页面内嵌数据，当前共 ' + store.size + ' 条'); return }
    if (name === 'clear') { store.clear(); seenUrls.length = 0; render(); return }
    if (name === 'diag') {
      const sample = store.size ? JSON.stringify(Array.from(store.values())[0], null, 2) : '(空)';
      const diag = {
        url: location.href,
        captured: store.size,
        endpoints: seenUrls,
        firstOrder: sample
      };
      const text = JSON.stringify(diag, null, 2);
      window.prompt('把这段发给开发者，就能对齐字段（Ctrl+C 复制）：', text);
      return;
    }
  }
  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
    document.body.appendChild(ta); ta.select();
    try { document.execCommand('copy'); done() } catch (e) { alert('复制失败，请用「导出 JSON」下载文件') }
    ta.remove();
  }

  // ---------- 启动 ----------
  function boot() {
    if (!document.body) { setTimeout(boot, 50); return }
    if (document.getElementById('xy-export-panel')) return;
    buildPanel();
    setTimeout(scanEmbedded, 1500);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
  window.addEventListener('load', function () { setTimeout(scanEmbedded, 800) });
})();
