import { api } from './api'

/**
 * 通用反馈动作工厂：把后端返回的 undo_token 包装成可挂到 toast 上的动作。
 * 任何删除 / 覆盖类操作只要返回 undo_token，都能复用这一套 UI 与错误处理。
 */
export function undoAction(token, opts = {}) {
  if (!token) return null
  return {
    label: opts.label || '撤销',
    icon: opts.icon || 'undo',
    ttl: opts.ttl || 9000,
    run: async () => {
      await api.undo(token)
      if (opts.after) await opts.after()
      if (opts.notify) opts.notify(opts.doneText || '已撤销，数据已恢复')
    }
  }
}

/** 任意异步动作都能挂到 toast 上（可配置文案、图标、停留时长） */
export function toastAction(label, run, opts = {}) {
  return { label, icon: opts.icon || null, ttl: opts.ttl || 9000, run }
}
