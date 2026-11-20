const DAILY_ENABLED_KEY = 'xy-daily-update-check'
const LAST_CHECK_KEY = 'xy-daily-update-last-check'
const NOTIFIED_RELEASE_KEY = 'xy-update-notified-release'

let memoryEnabled = false
let memoryLastCheck = ''
let memoryNotifiedRelease = ''

function read(key, fallback = '') {
  try {
    return localStorage.getItem(key) || fallback
  } catch (e) {
    return fallback
  }
}

function write(key, value) {
  try {
    localStorage.setItem(key, value)
  } catch (e) {
    /* 隐私模式下仍用内存状态，至少保证本次会话不重复 */
  }
}

function localDay(date = new Date()) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

export function dailyUpdateCheckEnabled() {
  return read(DAILY_ENABLED_KEY) === '1' || memoryEnabled
}

export function setDailyUpdateCheckEnabled(enabled) {
  memoryEnabled = !!enabled
  write(DAILY_ENABLED_KEY, enabled ? '1' : '0')
}

export function lastDailyUpdateCheck() {
  return read(LAST_CHECK_KEY, memoryLastCheck)
}

export function dailyUpdateCheckedToday() {
  const value = lastDailyUpdateCheck()
  if (!value) return false
  const date = new Date(value)
  return Number.isFinite(date.getTime()) && localDay(date) === localDay()
}

export function markDailyUpdateChecked() {
  memoryLastCheck = new Date().toISOString()
  write(LAST_CHECK_KEY, memoryLastCheck)
}

// 先占位再发请求，多个标签页同时打开时也只会有一个真正执行每日检查。
export function claimDailyUpdateCheck() {
  if (!dailyUpdateCheckEnabled() || dailyUpdateCheckedToday()) return false
  const stamp = new Date().toISOString()
  memoryLastCheck = stamp
  write(LAST_CHECK_KEY, stamp)
  return lastDailyUpdateCheck() === stamp
}

export function dailyUpdateCheckHint() {
  const value = lastDailyUpdateCheck()
  if (!value) return '尚未检查'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '尚未检查'
  return `上次检查 ${date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  })}`
}

function releaseToken(result) {
  const version = String(result?.latest || '').trim()
  if (!version) return ''
  return `${String(result?.repo || 'default').trim()}@${version}`
}

export function updateReleaseNotified(result) {
  const token = releaseToken(result)
  return !!token && (read(NOTIFIED_RELEASE_KEY, memoryNotifiedRelease) === token)
}

export function markUpdateReleaseNotified(result) {
  const token = releaseToken(result)
  if (!token) return
  memoryNotifiedRelease = token
  write(NOTIFIED_RELEASE_KEY, token)
}
