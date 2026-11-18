// 多人实时同步：SSE 客户端。
//
// 只负责「感知别人改了什么」——变更事件不携带数据，收到后由调用方走既有 API
// 重拉，避免双写路径。浏览器 EventSource 自带断线重连，服务端配合
// Last-Event-ID 补发断线期间的变更。
import { ref } from 'vue'
import { clientId } from './clientId'
import { currentShopId } from './shopId'

export const online = ref(0)      // 当前在线客户端数
export const connected = ref(false)
export const revision = ref(0)    // 服务端数据版本，单调递增

let es = null
const listeners = new Set()

export function onRealtime(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function emit(ev) {
  for (const fn of listeners) {
    try { fn(ev) } catch (e) { /* 单个订阅异常不影响其它订阅 */ }
  }
}

function applyMeta(d) {
  if (typeof d?.online === 'number') online.value = d.online
  if (typeof d?.rev === 'number') revision.value = d.rev
}

/**
 * 建立实时连接。店铺通过 query 传入 —— EventSource 无法自定义请求头，
 * 服务端 resolveShop 同时支持 X-Shop-Id 与 ?shop=，两种情况共用一套校验。
 * 重复调用会先断开旧连接：切换店铺必须换一条流，否则会继续收到别店的事件。
 */
export function connectRealtime() {
  if (typeof EventSource === 'undefined') return
  if (es) { es.close(); es = null }
  const sid = currentShopId()
  const url = sid ? `/api/events?shop=${sid}` : '/api/events'
  try {
    es = new EventSource(url)
  } catch (e) {
    es = null
    return
  }
  es.addEventListener('open', () => { connected.value = true })
  es.addEventListener('hello', (e) => {
    connected.value = true
    try { applyMeta(JSON.parse(e.data)) } catch (err) { /* ignore */ }
  })
  es.addEventListener('presence', (e) => {
    try { applyMeta(JSON.parse(e.data)) } catch (err) { /* ignore */ }
  })
  es.addEventListener('change', (e) => {
    let d = null
    try { d = JSON.parse(e.data) } catch (err) { return }
    applyMeta(d)
    emit(d)
  })
  // 断线时浏览器会自动重连，这里只更新指示状态，不弹错
  es.addEventListener('error', () => { connected.value = false })
}

export function disconnectRealtime() {
  if (es) {
    es.close()
    es = null
  }
  connected.value = false
}

export { clientId }
