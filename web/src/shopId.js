// 当前店铺 id 的唯一真相源。
//
// 单独成文件是为了断开循环依赖：api.js 需要读它来决定 X-Shop-Id，
// 而店铺列表的加载又依赖 api。这里只做读写，不引任何模块。

const KEY = 'xy-shop'
let cached = null

/** 当前店铺 id；0 表示「还没选过」，请求时不带 header，由服务端回落默认店铺。 */
export function currentShopId() {
  if (cached !== null) return cached
  try {
    const v = parseInt(localStorage.getItem(KEY) || '', 10)
    cached = Number.isFinite(v) && v > 0 ? v : 0
  } catch (e) {
    cached = 0
  }
  return cached
}

export function setShopId(id) {
  const n = Number(id) || 0
  cached = n > 0 ? n : 0
  try {
    if (cached) localStorage.setItem(KEY, String(cached))
    else localStorage.removeItem(KEY)
  } catch (e) { /* 隐私模式下忽略 */ }
}

// 本次浏览器会话是否已主动选过店铺。
// 用 sessionStorage 而不是 localStorage：只要系统里存在店铺密码，
// 每次新开页面都要重新选一次进哪家店，但同一会话内刷新不必重复问。
const SESSION_KEY = 'xy-shop-picked'

export function markShopPicked() {
  try { sessionStorage.setItem(SESSION_KEY, '1') } catch (e) { /* 隐私模式忽略 */ }
}

export function shopPickedThisSession() {
  try { return sessionStorage.getItem(SESSION_KEY) === '1' } catch (e) { return false }
}

export function clearShopPicked() {
  try { sessionStorage.removeItem(SESSION_KEY) } catch (e) { /* ignore */ }
}
