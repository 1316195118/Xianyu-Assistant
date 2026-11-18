// 稳定的客户端标识：同一标签页内保持不变。
// 用途：实时同步事件里带 actor，前端据此忽略「自己刚触发的变更」，
// 避免自己保存后又被自己的事件刷一次列表。
const KEY = 'xy-client-id'
let id = ''
try { id = sessionStorage.getItem(KEY) || '' } catch (e) { /* 隐私模式可能禁用 */ }
if (!id) {
  id = globalThis.crypto?.randomUUID?.()
    || ('c' + Math.random().toString(36).slice(2) + Date.now().toString(36))
  try { sessionStorage.setItem(KEY, id) } catch (e) { /* ignore */ }
}
export const clientId = id
