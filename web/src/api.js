import { clientId } from './clientId'
import { currentShopId } from './shopId'

// 附件文件名形如 25_d740d385b3c44593.png，中间那段随机 hex 就是内容指纹。
// 拿它当 ?v= 传回去，服务端才敢给「一年强缓存」；换图后文件名会变，缓存自然失效。
export function attachVer(file) {
  const s = String(file || '')
  const m = /^\d+_([0-9a-f]+)\.[a-z]+$/i.exec(s)
  if (m) return m[1]
  return /^[0-9a-f]{8,}$/i.test(s) ? s : ''
}

async function req(url, opts = {}) {
  // 每个请求带上客户端标识，实时同步事件据此区分「谁改的」
  const { timeoutMs = 15000, ...rest } = opts
  const headers = { 'X-Client-Id': clientId, ...(rest.headers || {}) }
  // 店铺上下文：服务端据此在每条查询上加 shop_id 条件。
  // 没选过店铺时不带，由服务端回落默认店铺（兼容老书签与脚本）。
  const sid = currentShopId()
  if (sid) headers['X-Shop-Id'] = String(sid)
  // 请求超时：服务重启、网络半断时 fetch 会一直挂着，页面就永远停在「正在加载…」。
  // 15 秒还没响应就当作失败往上抛，让页面能显示错误而不是无限转圈。
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), timeoutMs)
  let res
  try {
    res = await fetch(url, { ...rest, headers, signal: ctrl.signal })
  } catch (e) {
    clearTimeout(timer)
    if (e && e.name === 'AbortError') throw new Error('请求超时，请检查网络或刷新重试')
    throw new Error('网络连接失败，请检查网络后重试')
  }
  clearTimeout(timer)
  let data
  try {
    data = await res.json()
  } catch (e) {
    throw new Error('服务器返回异常')
  }
  if (!res.ok) {
    if (res.status === 401) {
      window.dispatchEvent(new Event('xy-unauthorized'))
    }
    const err = new Error(data?.error || `请求失败 (${res.status})`)
    err.status = res.status
    err.data = data      // 409 冲突时带 current，供编辑弹窗载入最新值
    throw err
  }
  return data
}

export const api = {
  version() { return req('/api/version') },
  shops() { return req('/api/shops') },
  authStatus() { return req('/api/auth/status') },
  shopLogin(shopId, password) {
    return req('/api/auth/shop-login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ shop_id: shopId, password })
    })
  },
  createShop(payload) {
    return req('/api/shops', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
  },
  updateShop(id, payload) {
    return req('/api/shops/' + id, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
  },
  deleteShop(id) {
    return req('/api/shops/' + id, { method: 'DELETE' })
  },
  stats(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/stats' + (q ? '?' + q : ''))
  },
  list(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/records' + (q ? '?' + q : ''))
  },
  // 返回 { merged: true|false, item }
  create(record) {
    return req('/api/records', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(record)
    })
  },
  update(id, record) {
    return req('/api/records/' + id, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(record)
    })
  },
  remove(id) {
    return req('/api/records/' + id, { method: 'DELETE' })
  },
  batchRemove(ids) {
    return req('/api/records/batch-delete', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids })
    })
  },
  undo(token) {
    return req('/api/undo', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token })
    })
  },
  trend(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/trend' + (q ? '?' + q : ''))
  },
  purchases() { return req('/api/purchases') },
  // 批量给卖单补成本：把选中的卖单挂到同一个买进单
  linkPurchases(data) {
    return req('/api/records/link-purchases', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) })
  },
  // 批量标记 / 取消「无成本」：代卖、寄卖、赠送这类本来就没有货品成本的卖单
  markCostFree(ids, free) {
    return req('/api/records/cost-free', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids, free }) })
  },
  // 智能挂成本：先取匹配建议（不写库），用户确认后再 apply
  // 闲鱼扫码登录
  goofishQRStart() { return req('/api/goofish/qr/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }) },
  goofishQRPoll() { return req('/api/goofish/qr/poll', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }) },
  // 待办角标：未挂成本卖单数
  todoCount() { return req('/api/records/todo-count') },
  // 月度利润目标（按店铺按月）
  getMonthlyTarget(month) { return req('/api/monthly-target' + (month ? '?month=' + month : '')) },
  saveMonthlyTarget(month, target) {
    return req('/api/monthly-target', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ month, target }) })
  },
  // 同商品历史成交价：录入时给定价参考
  priceHint(product) { return req('/api/records/price-hint?product=' + encodeURIComponent(product || '')) },
  autoLinkPreview() { return req('/api/records/auto-link/preview') },
  autoLinkApply(links) {
    return req('/api/records/auto-link/apply', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ links }) })
  },
  // 没有买进单的卖单：按售价比例估算后直接写单件成本
  estimateCost(items) {
    return req('/api/records/estimate-cost', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ items }) })
  },
  // ---------- 联单（N:M 多买多卖） ----------
  productProfit() { return req('/api/product-profit') },
  groups() { return req('/api/groups') },
  // 只取「id / 标题 / 完结状态 / 成员 id」，给明细和概览打标签用。
  // 完整版会把每个联单的商品明细和统计都算一遍，一个列表页没必要付这个代价。
  groupsBrief() { return req('/api/groups?brief=1') },
  group(id) { return req('/api/groups/' + id) },
  createGroup(data) {
    return req('/api/groups', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) })
  },
  updateGroup(id, data) {
    return req('/api/groups/' + id, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) })
  },
  removeGroup(id) { return req('/api/groups/' + id, { method: 'DELETE' }) },
  // 往已有联单里追加记录（幂等，不会覆盖别人刚加的成员）
  addGroupItems(id, itemIds) {
    return req('/api/groups/' + id + '/items', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ item_ids: itemIds })
    })
  },
  // 只保存外部成交那一块（kind: sell | buy）
  saveGroupExternals(id, kind, items) {
    return req('/api/groups/' + id + '/externals', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kind, items })
    })
  },
  settleGroup(id, settled) {
    return req('/api/groups/' + id + '/settle', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ settled })
    })
  },
  groupCandidates(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/groups/candidates' + (q ? '?' + q : ''))
  },
  groupsByItem(id) { return req('/api/groups/by-item/' + id) },
  autoMergeGroups() {
    return req('/api/groups/auto-merge', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
  },
  // 合并建议：先给建议（相似标题的联单 / 同名散单），用户挑完再提交
  groupMergeSuggestions() { return req('/api/groups/merge-suggestions') },
  applyGroupMerge(data) {
    return req('/api/groups/merge-apply', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data)
    })
  },
  linkSale(data) {
    return req('/api/groups/link-sale', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data)
    })
  },
  async recognize(file) {
    const fd = new FormData()
    fd.append('image', file)
    // 与其它请求一致地声明客户端与店铺；multipart 不能手写 Content-Type
    const h = { 'X-Client-Id': clientId }
    const sid = currentShopId()
    if (sid) h['X-Shop-Id'] = String(sid)
    const res = await fetch('/api/recognize', { method: 'POST', headers: h, body: fd })
    let data
    try {
      data = await res.json()
    } catch (e) {
      throw new Error('服务器返回异常')
    }
    if (!res.ok) {
      throw new Error(data?.error || `识别失败 (${res.status})`)
    }
    return data
  },
  parseText(text) {
    return req('/api/parse-text', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text })
    })
  },
  // ---------- 设置 ----------
  getSettings() { return req('/api/settings') },
  saveSettings(s) {
    return req('/api/settings', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(s)
    })
  },
  testSettings() {
    return req('/api/settings/test', { method: 'POST' })
  },
  // ---------- 鉴权 ----------
  authStatus() { return req('/api/auth/status') },
  login(password) {
    return req('/api/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password })
    })
  },
  logout() { return req('/api/auth/logout', { method: 'POST' }) },
  setPassword(data) {
    return req('/api/auth/password', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data)
    })
  },
  // ---------- 在线更新 ----------
  updateCheck() { return req('/api/update/check', { timeoutMs: 30000 }) },
  updateApply() {
    return req('/api/update/apply', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', timeoutMs: 120000
    })
  },
  saveUpdateConfig(payload) {
    return req('/api/update/config', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload)
    })
  },
  // ---------- 备份 ----------
  getBackups() { return req('/api/backups') },
  saveBackupConfig(cfg) {
    return req('/api/backups', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(cfg)
    })
  },
  runBackup() { return req('/api/backups', { method: 'POST' }) },
  deleteBackup(name) { return req('/api/backups/' + encodeURIComponent(name), { method: 'DELETE' }) },
  backupUrl(name) { return '/api/backups/' + encodeURIComponent(name) },
  // ---------- 导出 / 导入 ----------
  // 导出走浏览器直链（location.href / <a download>），无法携带 X-Shop-Id，
  // 所以必须把店铺拼进 query。否则在店铺 B 点导出，拿到的是默认店铺的数据。
  exportUrl(format, columns) {
    const p = new URLSearchParams()
    if (format === 'csv' || format === 'profit' || format === 'xlsx') p.set('format', format)
    if (columns && columns.length) p.set('columns', columns.join(','))
    const sid = currentShopId()
    if (sid) p.set('shop', String(sid))
    const q = p.toString()
    return '/api/export' + (q ? '?' + q : '')
  },
  // 批量导入前查重：把订单号丢给服务端，返回已存在的 {订单号: 记录id}
  existsOrders(orderNos) {
    return req('/api/records/exists', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ order_nos: orderNos })
    })
  },
  importData(mode, data) {
    return req('/api/import', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode, data })
    })
  },
  // ---------- 支付宝账单导入（手续费按订单号、快递费按发货时间） ----------
  alipayPreview(file) {
    const fd = new FormData()
    fd.append('file', file)
    return req('/api/alipay/preview', { method: 'POST', body: fd })
  },
  alipayApply(payload) {
    return req('/api/alipay/apply', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
  },
  alipayUndo() { return req('/api/alipay/undo', { method: 'POST' }) },
  // 单商品完结 / 弃单（on=true 生效，false 取消）
  settleRecord(id, on) {
    return req('/api/records/' + id + '/settle', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ on: !!on })
    })
  },
  discardRecord(id, on) {
    return req('/api/records/' + id + '/discard', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ on: !!on })
    })
  },
  // ---------- 闲鱼同步 ----------
  goofishStatus() { return req('/api/goofish/status') },
  goofishLogin(cookie) {
    return req('/api/goofish/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cookie })
    })
  },
  goofishLogout() { return req('/api/goofish/logout', { method: 'POST' }) },
  goofishPing() { return req('/api/goofish/ping', { method: 'POST' }) },
  goofishAuto(days, window, backfill) {
    const body = { days, window: window || 0 }
    if (typeof backfill === 'boolean') body.backfill = backfill
    return req('/api/goofish/auto', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })
  },
  goofishKeepAlive(on) {
    return req('/api/goofish/keepalive', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ on: !!on })
    })
  },
  goofishPreview(months, backfillMonths) {
    return req('/api/goofish/preview', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ months, backfill_months: backfillMonths || 0 })
    })
  },
  goofishApply(payload) {
    return req('/api/goofish/apply', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
  },
  // ---------- 开源许可 ----------
  getLicenses() { return req('/licenses/index.json') },
  // ---------- 1:N 匹配 ----------
  match(payload) {
    return req('/api/match', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload)
    })
  },
  // ---------- 运费标签 ----------
  getTags() { return req('/api/tags') },
  addTag(name) {
    return req('/api/tags', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name })
    })
  },
  removeTag(id) { return req('/api/tags/' + id, { method: 'DELETE' }) },
  // ---------- 增强能力 ----------
  undoOps() { return req('/api/undo/ops') },
  discardUndo(token) {
    return req('/api/undo/ops?token=' + encodeURIComponent(token), { method: 'DELETE' })
  },
  batchUpdate(ids, fields) {
    return req('/api/records/batch-update', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids, fields })
    })
  },
  statsExtras() { return req('/api/stats/extras') },
  consignStats(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/consign/stats' + (q ? '?' + q : ''))
  },
  consignOwners() { return req('/api/consign/owners') },
  consignSettle(payload) {
    return req('/api/consign/settle', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload)
    })
  },
  consignSettlements(params = {}) {
    const q = new URLSearchParams(params).toString()
    return req('/api/consign/settlements' + (q ? '?' + q : ''))
  },
  deleteConsignSettlement(id) {
    return req('/api/consign/settlements/' + id, { method: 'DELETE' })
  },
  ocrFeedback(data) {
    return req('/api/ocr-feedback', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data)
    })
  },
  ocrFeedbackList() { return req('/api/ocr-feedback') },
  verifyBackup() { return req('/api/backup/verify') },
  health() { return req('/api/health') },
  attachmentUrl(id, opts = {}) {
    // w=缩略图宽度（手机上列表只要几十像素，没必要拉 1MB 原图）
    // file=记录里的附件文件名，用来取版本号，命中服务端一年强缓存
    const p = new URLSearchParams()
    if (opts.w) p.set('w', String(opts.w))
    const v = attachVer(opts.file)
    if (v) p.set('v', v)
    const q = p.toString()
    return '/api/records/' + id + '/attachment' + (q ? '?' + q : '')
  },
  uploadAttachment(id, file) {
    const fd = new FormData()
    fd.append('image', file)
    // 必须带 X-Client-Id：服务端据此标注事件发起者，缺了它，
    // 自己上传的附件会被实时通道当成「别人改了」并弹提示。
    // 同理要带 X-Shop-Id，否则全局会话下会落到默认店铺。
    // 注意不能设 Content-Type，交给浏览器生成 multipart boundary。
    const headers = { 'X-Client-Id': clientId }
    const sid = currentShopId()
    if (sid) headers['X-Shop-Id'] = String(sid)
    return fetch('/api/records/' + id + '/attachment', { method: 'POST', headers, body: fd })
      .then(async (r) => {
        const d = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(d.error || '上传失败')
        return d
      })
  }
}

/**
 * 下载导出文件。
 *
 * 以前是 `location.href = '/api/export?...'` 一步直连，浏览器接管后就再没有
 * 任何信号了：手机上下载慢，页面毫无变化，很容易以为没点上、连点三次。
 * 这里改成 fetch 拿 Blob，页面能明确知道「开始 / 完成 / 失败」三个状态。
 * 明细几十 KB，一次性读进内存没有压力。
 */
export async function downloadFile(url, fallbackName = 'download') {
  const res = await fetch(url, { credentials: 'same-origin' })
  if (!res.ok) {
    let msg = `导出失败（${res.status}）`
    try {
      const j = await res.json()
      if (j?.error) msg = j.error
    } catch (e) { /* 非 JSON 错误体，用默认文案 */ }
    throw new Error(msg)
  }
  const blob = await res.blob()
  // 服务端在 Content-Disposition 里给了带店铺与时间的文件名，优先沿用
  let name = fallbackName
  const cd = res.headers.get('Content-Disposition') || ''
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd)
  const plain = /filename="?([^";]+)"?/i.exec(cd)
  if (star) { try { name = decodeURIComponent(star[1]) } catch (e) { name = fallbackName } }
  else if (plain) name = plain[1]
  const href = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = href
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(href), 4000)
  return { name, size: blob.size }
}
