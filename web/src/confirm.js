import { reactive, computed } from 'vue'

/**
 * 全局确认框状态。ConfirmHost.vue 订阅这里渲染主题化弹窗，
 * 业务代码只调用 confirm(options) 并 await 结果即可。
 *
 * 用队列而不是单个对象：极端情况下连续触发多个确认时不会互相覆盖，
 * 会依次弹出、依次 resolve。
 */
export const confirmState = reactive({ queue: [] })

let seq = 0

/**
 * 打开确认框，返回 Promise<boolean>（确定 = true，取消/遮罩/Esc = false）。
 *
 * @param {object|string} options 传字符串时等价于 { message }
 * @param {string} options.title        标题
 * @param {string} options.message      主体说明
 * @param {string|string[]} options.detail  补充信息（逐行显示，如订单号）
 * @param {string} options.confirmText  确定按钮文案，默认「确定」
 * @param {string} options.cancelText   取消按钮文案，默认「取消」
 * @param {boolean} options.danger      危险操作：红色主按钮 + 图标，默认焦点落在「取消」
 */
export function confirm(options = {}) {
  const o = typeof options === 'string' ? { message: options } : (options || {})
  return new Promise((resolve) => {
    confirmState.queue.push({
      id: ++seq,
      title: o.title || '请确认',
      message: o.message || '',
      detail: Array.isArray(o.detail) ? o.detail : (o.detail ? [o.detail] : []),
      confirmText: o.confirmText || '确定',
      cancelText: o.cancelText || '取消',
      danger: !!o.danger,
      resolve
    })
  })
}

/** 由 ConfirmHost 调用：取出并结算指定确认框 */
export function settleConfirm(id, value) {
  const i = confirmState.queue.findIndex((x) => x.id === id)
  if (i === -1) return
  const [item] = confirmState.queue.splice(i, 1)
  item.resolve(!!value)
}

/** 当前应显示的确认框（队首）；队列为空时为 null */
export const currentConfirm = computed(() => confirmState.queue[0] || null)
