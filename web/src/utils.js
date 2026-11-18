export function fmtMoney(n) {
  const v = Number(n || 0)
  const sign = v < 0 ? '-' : ''
  return sign + '¥' + Math.abs(v).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

export function fmtMoneyPlain(n) {
  const v = Number(n || 0)
  return v.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

export function fmtNum(n) {
  return Number(n || 0).toLocaleString('zh-CN')
}

export function shortTime(s) {
  if (!s) return '—'
  return String(s).replace('T', ' ').slice(0, 16)
}

export function todayStr() {
  const d = new Date()
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
