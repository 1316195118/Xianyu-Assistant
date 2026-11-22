<script setup>
import { ref, reactive, computed, watch, onMounted, onUnmounted, defineAsyncComponent } from 'vue'
import { api, downloadFile } from '../api'
import { confirm } from '../confirm'
import { themeMode, motionMode, setThemeInstant } from '../theme'
import HelpTip from '../components/HelpTip.vue'
import { undoAction } from '../feedback'
import { gfPanelOpen, gfStatus } from '../gfState'
import { activeId, activeShop } from '../shopStore'

// 设置页只保留入口，弹窗内容全部按需加载。闲鱼同步 / 支付宝导入这类
// 面板会连带二维码、解析表单和业务组件，放首包会让设置页明显变重。
const RecyclePanel = defineAsyncComponent(() => import('../components/RecyclePanel.vue'))
const VersionPanel = defineAsyncComponent(() => import('../components/VersionPanel.vue'))
const ShopManage = defineAsyncComponent(() => import('../components/ShopManage.vue'))
const GoofishSync = defineAsyncComponent(() => import('../components/GoofishSync.vue'))
const AlipayImport = defineAsyncComponent(() => import('../components/AlipayImport.vue'))
function preloadSettingsPanels() {
  const loaders = [
    () => import('../components/RecyclePanel.vue'),
    () => import('../components/VersionPanel.vue'),
    () => import('../components/ShopManage.vue'),
    () => import('../components/GoofishSync.vue'),
    () => import('../components/AlipayImport.vue')
  ]
  const run = () => { for (const load of loaders) load().catch(() => {}) }
  if (window.requestIdleCallback) window.requestIdleCallback(run, { timeout: 2500 })
  else setTimeout(run, 1200)
}

const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['notify', 'refresh'])

const themeOptions = [
  { v: 'system', t: '跟随系统', icon: 'M3 4h18v12H3zM8 20h8M12 16v4' },
  { v: 'light', t: '浅色', icon: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5.6 5.6 4.2 4.2M19.8 19.8l-1.4-1.4M18.4 5.6l1.4-1.4M4.2 19.8l1.4-1.4M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z' },
  { v: 'dark', t: '深色', icon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z' }
]
const themeLabel = computed(() => ({ system: '跟随系统', light: '浅色', dark: '深色' }[themeMode.value]))

// 系统是否已经开着「减少动态效果」。很多手机默认开启，此时「跟随系统」和「减少动效」
// 的实际效果完全一样，点下去画面不会有任何变化 —— 以前就静默无反应，看着像按钮坏了。
const sysReduce = ref(false)
let motionMq = null
let motionHandler = null
function readSysReduce() { sysReduce.value = !!(motionMq && motionMq.matches) }
onMounted(() => {
  if (typeof window === 'undefined' || !window.matchMedia) return
  motionMq = window.matchMedia('(prefers-reduced-motion: reduce)')
  readSysReduce()
  motionHandler = () => readSysReduce()
  if (motionMq.addEventListener) motionMq.addEventListener('change', motionHandler)
  else if (motionMq.addListener) motionMq.addListener(motionHandler)
})
onUnmounted(() => {
  if (!motionMq || !motionHandler) return
  if (motionMq.removeEventListener) motionMq.removeEventListener('change', motionHandler)
  else if (motionMq.removeListener) motionMq.removeListener(motionHandler)
  motionHandler = null
})
const motionEffective = computed(() =>
  motionMode.value === 'reduce' || (motionMode.value === 'system' && sysReduce.value) ? 'reduce' : 'full')

// 三个主题按钮：选中的原地不动作出说明，选别的就正常切换
function pickTheme(v, e) {
  if (themeMode.value === v) {
    emit('notify', `当前已经是「${themeLabel.value}」`, false, null, { tone: 'info' })
    return
  }
  // 从点击位置圆形扩散到全屏；不支持 View Transitions 或用户关了动效时直接切
  const doc = document
  const reduce = motionEffective.value === 'reduce' ||
    (typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches)
  if (!doc.startViewTransition || reduce) { themeMode.value = v; return }
  const x = e && typeof e.clientX === 'number' ? e.clientX : innerWidth / 2
  const y = e && typeof e.clientY === 'number' ? e.clientY : innerHeight / 2
  const r = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y))
  const vt = doc.startViewTransition(() => setThemeInstant(v))
  vt.ready.then(() => {
    doc.documentElement.animate(
      { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${r}px at ${x}px ${y}px)`] },
      { duration: 420, easing: 'cubic-bezier(.2,.8,.2,1)', pseudoElement: '::view-transition-new(root)' }
    )
  }).catch(() => {})
}
function pickMotion(v) {
  const before = motionEffective.value
  motionMode.value = v
  let msg
  if (v === 'system') {
    msg = sysReduce.value ? '已跟随系统：系统开启了「减少动态效果」' : '已跟随系统：当前为完整动效'
  } else {
    msg = before === 'reduce' ? '已经是减少动效了' : '已切换为减少动效'
  }
  emit('notify', msg, false, null, { tone: 'info' })
}

const engine = ref('local')
const form = reactive({ ocr_api_base: '', ocr_model: '', ocr_api_key: '' })
const meta = ref({ has_key: false, masked_key: '', source: 'env', env_fallback: false })
// 月度利润目标（按店铺按月）
const now = new Date()
const targetMonth = ref(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`)
const targetValue = ref(null)
const targetSaving = ref(false)
const targetLoading = ref(false)
const targetLoadedMonth = ref('')
const targetReadError = ref('')
const targetMsg = ref(null)
const canSaveTarget = computed(() => !targetSaving.value && !targetLoading.value &&
  !!targetMonth.value && targetLoadedMonth.value === targetMonth.value &&
  typeof targetValue.value === 'number' && Number.isFinite(targetValue.value) && targetValue.value >= 0)
let targetSeq = 0
async function loadTarget() {
  const seq = ++targetSeq
  const month = targetMonth.value
  targetLoadedMonth.value = ''; targetValue.value = null
  targetMsg.value = null; targetReadError.value = ''
  targetLoading.value = !!month
  if (!month) return
  try {
    const r = await api.getMonthlyTarget(month)
    if (seq !== targetSeq) return
    targetValue.value = Number(r?.target) || 0
    targetLoadedMonth.value = month
  } catch (e) {
    if (seq === targetSeq) targetReadError.value = `读取目标失败：${e.message || '请重试'}`
  } finally {
    if (seq === targetSeq) targetLoading.value = false
  }
}
async function saveTarget() {
  if (!canSaveTarget.value) return
  const month = targetMonth.value
  targetSaving.value = true; targetMsg.value = null
  try {
    await api.saveMonthlyTarget(month, targetValue.value)
    targetMsg.value = { ok: true, text: '已保存月度目标' }
    emit('notify', '已保存月度目标')
    emit('refresh')
  } catch (e) { targetMsg.value = { ok: false, text: `保存失败：${e.message || '请重试'}` } }
  finally { targetSaving.value = false }
}
watch(targetMonth, loadTarget)

const advancedOpen = ref(false)
const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const msg = ref(null)

const licenses = ref([])
const licensesOpen = ref(false)
const recycleOpen = ref(false)  // 回收站二级窗口（设置页只留入口，避免大块列表占位）
const versionOpen = ref(false)  // 版本信息二级窗口
const shopOpen = ref(false)     // 店铺管理二级窗口
const gfOpen = ref(false)
const appearanceOpen = ref(false)  // 外观与动效二级窗口
const engineOpen = ref(false)      // 识别引擎二级窗口
// 顶栏「去绑定」请求的入口：组件可能是懒加载的，immediate 保证挂载时请求还没丢
watch(gfPanelOpen, (v) => {
  if (!v) return
  gfOpen.value = true
  gfPanelOpen.value = false
}, { immediate: true })
const aliOpen = ref(false)   // 支付宝账单导入       // 闲鱼同步二级窗口
// 入口直接读 index.html 里的 xy-version meta（编译时注入，无需额外请求）
const versionLabel = computed(() => {
  try { return document.querySelector('meta[name="xy-version"]')?.content || '—' } catch (e) { return '—' }
})

// ---------- 访问密码 ----------
const auth = ref({ enabled: false })
const pwdOpen = ref(false)
const pwdForm = reactive({ old: '', password: '', password2: '' })
const pwdBusy = ref(false)
const pwdMsg = ref(null)

// ---------- 自动备份 ----------
const backupOpen = ref(false)
const backup = ref({ enabled: false, interval_hours: 24, keep: 7, last: '', dir: '', files: [] })
const backupBusy = ref(false)
const verifyBusy = ref(false)
const backupMsg = ref(null)
const backupError = ref('')   // 读取备份状态失败的原因（与「真的没备份」区分开）

// ---------- 数据导入导出 ----------
const dataOpen = ref(false)
const importMode = ref('merge')
const importBusy = ref(false)
const dataMsg = ref(null)
const excelBusy = ref(false)
const exporting = ref('')   // '' | 'json' | 'csv'：导出期间的按钮忙碌态
const fileInput = ref(null)
const csvInput = ref(null)
const readonlyPwd = ref('')
const helpOpen = ref(false)      // 识别与配对说明：默认收起
const readonlyOpen = ref(false)  // 只读账号二级窗口
const targetOpen = ref(false)    // 月度利润目标二级窗口
watch(targetOpen, (open) => { if (open && !targetSaving.value) loadTarget() })

async function saveReadonly() {
  // 管理密码是只读账号的前提：没有它，系统本来就不拦人，只读限制没有意义。
  if (!auth.value.enabled) {
    const ok = await confirm({
      title: '先设置管理密码',
      message: '只读账号依赖管理密码来区分「谁能改、谁只能看」。',
      detail: ['现在还没有管理密码，任何能打开这个地址的人都可以直接修改数据。', '请先设置管理密码（访问密码），再回来设只读密码。'],
      confirmText: '去设置管理密码'
    })
    if (ok) { readonlyOpen.value = false; pwdOpen.value = true }
    return
  }
  if ([...readonlyPwd.value].length < MIN_READONLY_PWD) { emit('notify', `只读密码至少 ${MIN_READONLY_PWD} 位`, true); return }
  try {
    await api.saveSettings({ readonly_password: readonlyPwd.value })
    readonlyPwd.value = ''
    await load()
    emit('notify', '只读密码已设置')
  } catch (e) { emit('notify', '设置失败：' + e.message, true) }
}
async function clearReadonly() {
  try {
    await api.saveSettings({ clear_readonly_password: true })
    await load()
    emit('notify', '只读密码已清除')
  } catch (e) { emit('notify', '清除失败：' + e.message, true) }
}
async function verifyBackupNow() {
  if (verifyBusy.value) return
  verifyBusy.value = true
  backupMsg.value = null
  try {
    const r = await api.verifyBackup()
    backupMsg.value = {
      text: r.consistent
        ? `对账一致：数据库 ${r.items_in_db} 条 / 最新备份 ${r.items_in_backup} 条`
        : `对账不一致：数据库 ${r.items_in_db} 条 / 备份 ${r.items_in_backup === null ? '无可用备份' : r.items_in_backup + ' 条'}`,
      ok: !!r.consistent
    }
    emit('notify', backupMsg.value.text, !r.consistent)
  } catch (e) {
    backupMsg.value = { text: '对账失败：' + e.message, ok: false }
    emit('notify', backupMsg.value.text, true)
  } finally { verifyBusy.value = false }
}

async function loadAuth() {
  try { auth.value = await api.authStatus() } catch (e) { /* ignore */ }
}
async function loadBackups() {
  backupError.value = ''
  try { backup.value = await api.getBackups() }
  catch (e) {
    // 读不到 ≠ 没备份过。把状态标成「未知」，界面上说清楚并给重试，
    // 别让首页/设置页把它当成「从未备份」吓人一跳。
    backupError.value = e.message || '备份状态读取失败'
  }
}

const presets = [
  { name: 'OpenAI', base: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
  { name: 'DeepSeek', base: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
  { name: '通义千问', base: 'https://dashscope.aliyuncs.com/compatible-mode/v1', model: 'qwen-vl-max' },
  { name: 'Moonshot', base: 'https://api.moonshot.cn/v1', model: 'moonshot-v1-8k-vision-preview' },
  { name: '本地 Ollama', base: 'http://localhost:11434/v1', model: 'qwen2.5vl' }
]

async function load() {
  loading.value = true
  try {
    const s = await api.getSettings()
    engine.value = s.ocr_engine || 'local'
    advancedOpen.value = engine.value === 'vision'
    form.ocr_api_base = s.ocr_api_base || ''
    form.ocr_model = s.ocr_model || ''
    form.ocr_api_key = ''
    meta.value = s
  } catch (e) {
    msg.value = { text: '读取配置失败：' + e.message, ok: false }
  } finally { loading.value = false }
}
async function loadLicenses() {
  try { licenses.value = await api.getLicenses() } catch (e) { /* ignore */ }
}
onMounted(() => {
  load(); loadLicenses(); loadAuth(); loadBackups()
  // 先保证设置页首屏轻量，空闲后再预取弹窗，常见入口首次点击也能秒开。
  preloadSettingsPanels()
})
// 撤销覆盖导入等整库级操作后，由 App 提升 refreshKey 触发本页重载
watch(() => props.refreshKey, () => { load(); loadBackups(); loadAuth() })

// ---------- 访问密码 ----------
// 服务端要求管理密码 ≥8 位、只读密码 ≥4 位。两边必须同一个数，
// 否则页面说「4 位就行」、提交却被后端打回来，用户只会觉得按钮坏了。
const MIN_ADMIN_PWD = 8
const MIN_READONLY_PWD = 4
async function savePassword() {
  pwdMsg.value = null
  if (!pwdForm.password) { pwdMsg.value = { text: '请输入新密码', ok: false }; return }
  if (pwdForm.password !== pwdForm.password2) { pwdMsg.value = { text: '两次输入的新密码不一致', ok: false }; return }
  if ([...pwdForm.password].length < MIN_ADMIN_PWD) {
    pwdMsg.value = { text: `管理密码至少 ${MIN_ADMIN_PWD} 位（建议字母 + 数字）`, ok: false }
    return
  }
  pwdBusy.value = true
  try {
    await api.setPassword({ old: pwdForm.old, password: pwdForm.password })
    pwdForm.old = ''; pwdForm.password = ''; pwdForm.password2 = ''
    await loadAuth()
    pwdMsg.value = { text: '访问密码已启用并保存，当前登录状态保持有效', ok: true }
    emit('notify', '访问密码已启用')
  } catch (e) {
    pwdMsg.value = { text: '保存失败：' + e.message, ok: false }
  } finally { pwdBusy.value = false }
}
async function disablePassword() {
  const ok = await confirm({
    title: '关闭访问密码？',
    message: '关闭后任何能访问该地址的人都可以查看和修改数据。',
    detail: '稍后可在「访问密码」中重新开启。',
    confirmText: '关闭密码', danger: true
  })
  if (!ok) return
  if (!pwdForm.old) { pwdMsg.value = { text: '请输入当前密码以确认关闭', ok: false }; return }
  pwdBusy.value = true
  try {
    await api.setPassword({ old: pwdForm.old, disable: true })
    pwdForm.old = ''
    await loadAuth()
    await load()   // 服务端会一并清掉只读密码，刷新徽标
    pwdMsg.value = { text: '访问密码已关闭，只读密码也已清除', ok: true }
    emit('notify', '访问密码已关闭，只读密码也已一并清除', false, null, { tone: 'warn', ttl: 6000 })
  } catch (e) {
    pwdMsg.value = { text: '操作失败：' + e.message, ok: false }
  } finally { pwdBusy.value = false }
}

// ---------- 自动备份 ----------
async function saveBackupConfig() {
  backupBusy.value = true; backupMsg.value = null
  try {
    await api.saveBackupConfig({
      enabled: backup.value.enabled,
      interval_hours: Number(backup.value.interval_hours),
      keep: Number(backup.value.keep)
    })
    await loadBackups()
    backupMsg.value = { text: '备份设置已保存', ok: true }
    emit('notify', '备份设置已保存')
  } catch (e) {
    backupMsg.value = { text: '保存失败：' + e.message, ok: false }
  } finally { backupBusy.value = false }
}
async function runBackupNow() {
  backupBusy.value = true; backupMsg.value = null
  try {
    const r = await api.runBackup()
    await loadBackups()
    backupMsg.value = { text: '备份完成：' + r.name, ok: true }
    emit('notify', '备份完成')
  } catch (e) {
    backupMsg.value = { text: '备份失败：' + e.message, ok: false }
  } finally { backupBusy.value = false }
}
async function deleteBackup(name) {
  const ok = await confirm({
    title: '删除备份文件？',
    message: name,
    detail: '删除后可在提示条中撤销（9 秒）。',
    confirmText: '删除', danger: true
  })
  if (!ok) return
  try {
    const res = await api.deleteBackup(name)
    await loadBackups()
    backupMsg.value = { text: '备份已删除', ok: true }
    emit('notify', '备份已删除', false, undoAction(res.undo_token, {
      after: loadBackups,
      notify: (t) => emit('notify', t),
      doneText: '备份文件已恢复'
    }))
  } catch (e) { backupMsg.value = { text: '删除失败：' + e.message, ok: false } }
}
function fmtSize(n) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

// ---------- 导入导出 ----------
// 同样走 fetch + Blob：页面能确认「真的下载完了」，而不只是「点了按钮」。
// 备份 JSON 可能几 MB，读完再落盘仍然是一次性内存占用，可接受。
async function doExport(format) {
  const name = { csv: ' CSV 表格', json: ' JSON 备份', xlsx: ' Excel 表格' }[format] || ''
  const label = { csv: '闲鱼明细', json: '闲鱼备份', xlsx: '闲鱼明细' }[format] || '闲鱼导出'
  const ext = { csv: 'csv', json: 'json', xlsx: 'xlsx' }[format] || 'dat'
  const stamp = new Date().toISOString().slice(0, 10)
  if (exporting.value) return false
  exporting.value = format
  dataMsg.value = { text: `正在导出${name}…`, ok: true }
  try {
    await downloadFile(api.exportUrl(format), `${label}-${stamp}.${ext}`)
    dataMsg.value = { text: `已导出${name}（仅当前店铺）`, ok: true }
    return true
  } catch (e) {
    dataMsg.value = { text: `导出${name}失败：${e.message || '网络异常'}`, ok: false }
    return false
  } finally {
    exporting.value = ''
  }
}
// Excel 改由服务端生成：表头配色、冻结首行、筛选、数字格式、状态配色、合计行
// 都是文件里的真样式。前端 SheetJS 社区版只能写值不能写样式，做不出「美化」。
function doExportExcel() {
  if (excelBusy.value) return
  excelBusy.value = true
  doExport('xlsx').then((ok) => {
    if (ok) emit('notify', 'Excel 已导出')
    setTimeout(() => { excelBusy.value = false }, 600)
  })
}
function pickFile() { if (!importBusy.value) fileInput.value?.click() }

async function submitImport(mode, parsed, format, shop) {
  const replacing = mode === 'replace'
  const detail = replacing
    ? ['覆盖会替换当前店铺的交易明细，原有记录的联单关联可能受影响。', '导入前会自动备份；覆盖完成后 15 秒内可在提示中撤销。']
    : ['追加不会清空现有记录；导入前会自动备份。']
  if (format === 'CSV' && csvPreview.value) {
    detail.unshift(`已识别 ${csvPreview.value.mapped} 列：${csvPreview.value.head.join('、')}` +
      (csvPreview.value.ignored.length ? `；忽略 ${csvPreview.value.ignored.length} 列` : ''))
  }
  const ok = await confirm({
    title: replacing ? '确认覆盖店铺数据？' : '确认追加数据？',
    message: `将从 ${format} 文件导入 ${parsed.items.length} 条记录到「${shop.name}」。`,
    detail,
    confirmText: replacing ? '确认覆盖' : '确认追加', danger: replacing
  })
  if (!ok) {
    dataMsg.value = { text: '已取消导入，未修改数据。', ok: true }
    return
  }
  if (activeId.value !== shop.id) throw new Error('店铺已切换，请重新选择导入文件')
  const r = await api.importData(mode, parsed)
  const text = r.message || `已导入 ${r.count ?? parsed.items.length} 条记录`
  dataMsg.value = { text, ok: true }
  emit('notify', text, false, undoAction(r.undo_token, {
    label: '撤销导入',
    ttl: 15000,
    doneText: '已撤销导入，数据恢复到导入前',
    after: async () => { await loadBackups(); emit('refresh') },
    notify: (t) => emit('notify', t)
  }))
  emit('refresh')
}

function importShop() {
  return { id: activeId.value, name: activeShop.value?.name || '当前店铺' }
}

// CSV 导入：把常见表头（中文或英文）映射成记录字段，再走和 JSON 一样的导入通道。
// 字段的显示名：预览里要让用户看懂「这一列会进到哪个字段」
const CSV_FIELD_LABELS = {
  product: '商品名称', sell_price: '成交价', sell_net: '到手价', sell_time: '卖出时间',
  sell_order_no: '卖出订单号', sell_counterparty: '买家', buy_price: '买进价',
  buy_time: '买进时间', buy_order_no: '买进订单号', buy_counterparty: '卖家',
  buy_shipping: '买运费', sell_shipping: '卖运费', sell_fee: '软件服务费',
  sell_wuyou_fee: '无忧卖服务费', note: '备注', quantity: '件数'
}
const csvPreview = ref(null)   // { cols, mapped, ignored, rows, count, head }
// 表头映射预览：把「哪一列会被认成什么」和「哪几列会被丢掉」摆出来，
// 否则列名写错时，只有导完发现金额串列才知道。
function buildCsvPreview(headers, keys, dataRows) {
  const cols = headers.map((h, i) => ({
    idx: i, name: h, field: keys[i] || '', label: CSV_FIELD_LABELS[keys[i]] || ''
  }))
  const mapped = cols.filter(c => c.field)
  const head = mapped.map(c => c.label)
  const sample = dataRows.slice(0, 3).map(cells => mapped.map(c => String(cells[c.idx] ?? '').trim()))
  csvPreview.value = {
    cols, head, rows: sample, count: dataRows.length,
    mapped: mapped.length, ignored: cols.filter(c => !c.field).map(c => c.name)
  }
}
const CSV_COLS = {
  '商品名称': 'product', '商品': 'product', 'product': 'product',
  '卖出价': 'sell_price', '成交价': 'sell_price', 'sell_price': 'sell_price',
  '到手价': 'sell_net', 'sell_net': 'sell_net',
  '卖出时间': 'sell_time', '交易时间': 'sell_time', 'sell_time': 'sell_time',
  '卖出订单号': 'sell_order_no', '订单号': 'sell_order_no', 'sell_order_no': 'sell_order_no',
  '买家': 'sell_counterparty', 'sell_counterparty': 'sell_counterparty',
  '买进价': 'buy_price', 'buy_price': 'buy_price',
  '买进时间': 'buy_time', 'buy_time': 'buy_time',
  '买进订单号': 'buy_order_no', 'buy_order_no': 'buy_order_no',
  '卖家': 'buy_counterparty', 'buy_counterparty': 'buy_counterparty',
  '买运费': 'buy_shipping', 'buy_shipping': 'buy_shipping',
  '卖运费': 'sell_shipping', 'sell_shipping': 'sell_shipping',
  '软件服务费': 'sell_fee', 'sell_fee': 'sell_fee',
  '无忧卖服务费': 'sell_wuyou_fee', 'sell_wuyou_fee': 'sell_wuyou_fee',
  '备注': 'note', 'note': 'note', '件数': 'quantity', 'quantity': 'quantity'
}
function parseCsv(text) {
  const rows = []
  for (const line of text.replace(/\r/g, '').split('\n')) {
    if (!line.trim()) continue
    const cells = []
    let cur = '', inQ = false
    for (const ch of line) {
      if (ch === '"') { inQ = !inQ; continue }
      if (ch === ',' && !inQ) { cells.push(cur); cur = ''; continue }
      cur += ch
    }
    cells.push(cur)
    rows.push(cells)
  }
  return rows
}
function pickCsv() { if (!importBusy.value) csvInput.value?.click() }
async function onCsvFile(e) {
  const f = e.target.files?.[0]
  e.target.value = ''
  if (!f || importBusy.value) return
  const mode = importMode.value, shop = importShop()
  importBusy.value = true
  dataMsg.value = null
  csvPreview.value = null
  try {
    const rows = parseCsv(await f.text())
    if (rows.length < 2) { dataMsg.value = { text: 'CSV 里没有数据行', ok: false }; return }
    const headers = rows[0].map((h) => String(h).trim())
    const keys = headers.map((h) => CSV_COLS[h] || CSV_COLS[h.toLowerCase()] || '')
    const items = []
    for (let i = 1; i < rows.length; i++) {
      const it = {}
      rows[i].forEach((cell, ci) => {
        const k = keys[ci]
        if (!k || cell === '') return
        const v = String(cell).trim()
        if (['buy_price', 'sell_price', 'sell_net', 'buy_shipping', 'sell_shipping', 'sell_fee', 'sell_wuyou_fee', 'quantity'].includes(k)) {
          it[k] = Number(v) || 0
        } else { it[k] = v }
      })
      if (!it.product && !it.sell_price && !it.buy_price) continue
      it.product = it.product || '未命名商品'
      if (!it.sell_time && !it.buy_time) {
        const t = new Date().toISOString().slice(0, 19).replace('T', ' ')
        if (it.sell_price) it.sell_time = t; else it.buy_time = t
      }
      items.push(it)
    }
    if (!items.length) { dataMsg.value = { text: '没有识别到可导入的列（需要 商品名称 + 金额 这类表头）', ok: false }; return }
    buildCsvPreview(headers, keys, rows.slice(1))
    await submitImport(mode, { items }, 'CSV', shop)
  } catch (err) {
    dataMsg.value = { text: 'CSV 导入失败：' + (err.message || err), ok: false }
  } finally { importBusy.value = false }
}

async function onImportFile(e) {
  const f = e.target.files?.[0]
  e.target.value = ''
  if (!f || importBusy.value) return
  const mode = importMode.value, shop = importShop()
  importBusy.value = true
  dataMsg.value = null
  try {
    const raw = await f.text()
    let parsed
    try { parsed = JSON.parse(raw) }
    catch { throw new Error('文件不是有效的 JSON') }
    if (!Array.isArray(parsed?.items)) throw new Error('缺少 items 数据，不是本系统导出的文件')
    const seenOrders = new Set()
    for (const it of parsed.items) {
      if (!it || typeof it !== 'object' || Array.isArray(it)) throw new Error('文件中有无法识别的记录')
      const no = String(it.sell_order_no || it.buy_order_no || '').trim()
      if (no && seenOrders.has(no)) throw new Error(`导入文件存在重复订单号 ${no}`)
      if (no) seenOrders.add(no)
      for (const k of ['buy_price', 'sell_price', 'sell_net', 'buy_shipping', 'sell_shipping']) {
        if (Number(it[k]) < 0) throw new Error(`订单 ${no || '—'} 的 ${k} 为负数`)
      }
      const t = String(it.buy_time || it.sell_time || '').trim()
      if (t && !/^\d{4}-\d{2}-\d{2}/.test(t)) throw new Error(`订单 ${no || '—'} 的时间格式不合法：${t}`)
    }
    await submitImport(mode, parsed, 'JSON', shop)
  } catch (err) {
    dataMsg.value = { text: 'JSON 导入失败：' + (err.message || err), ok: false }
  } finally { importBusy.value = false }
}

async function selectEngine(v) {
  engine.value = v
  advancedOpen.value = v === 'vision'
  msg.value = null
  try {
    await api.saveSettings({ ocr_engine: v })
    msg.value = { text: v === 'local' ? '已切换到本地 OCR（离线、免配置）' : '已切换到视觉大模型，请在进阶设置中确认 API 配置', ok: true }
    emit('notify', v === 'local' ? '识别引擎：本地 OCR' : '识别引擎：视觉大模型')
  } catch (e) {
    msg.value = { text: '切换失败：' + e.message, ok: false }
  }
}

function applyPreset(p) {
  form.ocr_api_base = p.base
  form.ocr_model = p.model
  msg.value = { text: `已填入 ${p.name} 预设，请补上 API Key 后保存`, ok: true }
}

async function save() {
  saving.value = true; msg.value = null
  try {
    await api.saveSettings({
      ocr_engine: engine.value,
      ocr_api_base: form.ocr_api_base.trim(),
      ocr_model: form.ocr_model.trim(),
      ocr_api_key: form.ocr_api_key.trim()
    })
    form.ocr_api_key = ''
    await load()
    advancedOpen.value = engine.value === 'vision'
    msg.value = { text: '配置已保存，立即生效（无需重启）', ok: true }
    emit('notify', '识别配置已保存')
  } catch (e) {
    msg.value = { text: '保存失败：' + e.message, ok: false }
  } finally { saving.value = false }
}

async function clearKey() {
  const ok = await confirm({
    title: '清除 API Key？',
    message: '已保存的视觉大模型 API Key 将从本机删除。',
    detail: '清除后视觉大模型需重新填入 Key 才能使用；本地 OCR 不受影响。',
    confirmText: '清除 Key', danger: true
  })
  if (!ok) return
  try {
    await api.saveSettings({
      ocr_engine: engine.value,
      ocr_api_base: form.ocr_api_base.trim(),
      ocr_model: form.ocr_model.trim(),
      ocr_api_key: '', clear_key: true
    })
    await load()
    advancedOpen.value = engine.value === 'vision'
    msg.value = { text: 'API Key 已清除', ok: true }
    emit('notify', 'API Key 已清除')
  } catch (e) { msg.value = { text: '操作失败：' + e.message, ok: false } }
}

async function test() {
  testing.value = true; msg.value = null
  if (form.ocr_api_key.trim()) {
    try {
      await api.saveSettings({
        ocr_engine: 'vision',
        ocr_api_base: form.ocr_api_base.trim(),
        ocr_model: form.ocr_model.trim(),
        ocr_api_key: form.ocr_api_key.trim()
      })
      form.ocr_api_key = ''
    } catch (e) {
      msg.value = { text: '保存配置失败：' + e.message, ok: false }; testing.value = false; return
    }
  }
  try {
    const r = await api.testSettings()
    msg.value = { text: r.message || '连接正常', ok: true }
  } catch (e) {
    msg.value = { text: e.message, ok: false }
  } finally { testing.value = false; load() }
}
</script>

<template>
  <div class="page settings-page">
    <!-- 常用 -->
    <section class="set-group">
      <div class="set-group-head"><b>常用</b><span>绑定账号、导入账单、设定目标</span></div>
      <div class="card set-group-items">
        <button class="set-row" aria-label="打开闲鱼同步" @click="gfOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/></svg></span>
          <span class="set-row-main"><b>闲鱼同步</b><small>扫码绑定，一键拉单与自动对账</small></span>
          <span class="badge" :class="gfStatus.order_api_valid ? 'badge-ok' : 'badge-buy'"><span class="dot"></span>{{ gfStatus.order_api_valid ? '可用' : (gfStatus.logged_in ? '需恢复' : '待绑定') }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开支付宝账单导入" @click="aliOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5-5 5 5"/><path d="M12 5v12"/></svg></span>
          <span class="set-row-main"><b>支付宝账单导入</b><small>按订单号补服务费与运费</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开月度利润目标" @click="targetOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.5"/><circle cx="12" cy="12" r="1"/></svg></span>
          <span class="set-row-main"><b>月度利润目标</b><small>首页显示本月完成进度</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
      </div>
    </section>

    <!-- 外观与识别 -->
    <section class="set-group">
      <div class="set-group-head"><b>外观与识别</b><span>界面显示与截图识别方式</span></div>
      <div class="card set-group-items">
        <button class="set-row" aria-label="打开外观与动效" @click="appearanceOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 0 0 18z" fill="currentColor" stroke="none"/></svg></span>
          <span class="set-row-main"><b>外观与动效</b><small>{{ themeLabel }} · {{ motionEffective === 'reduce' ? '减少动效' : '完整动效' }}</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开识别引擎" @click="engineOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 10h4M7 14h8"/></svg></span>
          <span class="set-row-main"><b>识别引擎</b><small>本地 OCR 不联网 / 视觉大模型更准</small></span>
          <span class="badge badge-ok"><span class="dot"></span>{{ engine === 'local' ? '本地 OCR' : '视觉大模型' }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开视觉大模型 API" @click="advancedOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 9l-4 3 4 3"/><path d="M16 9l4 3-4 3"/><path d="M14 5l-4 14"/></svg></span>
          <span class="set-row-main"><b>视觉大模型 API</b><small>仅在选用视觉模型时需要配置</small></span>
          <span class="badge" :class="meta.has_key ? 'badge-ok' : 'badge-buy'"><span class="dot"></span>{{ meta.has_key ? '已配置' : '未配置' }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开识别与配对说明" @click="helpOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M9.5 9a2.5 2.5 0 1 1 4 2c-.9.7-1.5 1.2-1.5 2.5"/><path d="M12 17h.01"/></svg></span>
          <span class="set-row-main"><b>识别与配对说明</b><small>买卖判定、自动配对与利润口径</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
      </div>
    </section>

    <!-- 店铺与安全 -->
    <section class="set-group">
      <div class="set-group-head"><b>店铺与安全</b><span>多店铺隔离与访问权限</span></div>
      <div class="card set-group-items">
        <button class="set-row" aria-label="管理店铺" @click="shopOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 9h16l-1 11H5L4 9z"/><path d="M8 9V6a4 4 0 0 1 8 0v3"/></svg></span>
          <span class="set-row-main"><b>店铺管理</b><small>新建、改名与删除店铺</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开访问密码" @click="pwdOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg></span>
          <span class="set-row-main"><b>访问密码</b><small>局域网 / 公网部署时建议开启</small></span>
          <span class="badge" :class="auth.enabled ? 'badge-ok' : 'badge-buy'"><span class="dot"></span>{{ auth.enabled ? '已开启' : '未开启' }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开只读账号设置" @click="readonlyOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6z"/><circle cx="12" cy="12" r="2.5"/></svg></span>
          <span class="set-row-main"><b>只读账号</b><small>独立密码，只能查看不能修改</small></span>
          <span class="badge" :class="!auth.enabled ? 'badge-buy' : (meta.has_readonly ? 'badge-ok' : 'badge-buy')"><span class="dot"></span>{{ !auth.enabled ? '需先设管理密码' : (meta.has_readonly ? '已开启' : '未设置') }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
      </div>
    </section>

    <!-- 数据与维护 -->
    <section class="set-group">
      <div class="set-group-head"><b>数据与维护</b><span>备份、迁移与误删恢复</span></div>
      <div class="card set-group-items">
        <button class="set-row" aria-label="打开自动备份" @click="backupOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/></svg></span>
          <span class="set-row-main"><b>自动备份</b><small>定时快照 · 上次 {{ backupError ? '读取失败' : (backup.last || '从未') }}</small></span>
          <span class="badge" :class="backupError ? 'badge-buy' : (backup.enabled ? 'badge-ok' : 'badge-buy')"><span class="dot"></span>{{ backupError ? '状态未知' : (backup.enabled ? '已开启' : '未开启') }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开数据导出与导入" @click="dataOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="M8 11l4 4 4-4"/><path d="M4 17v3h16v-3"/></svg></span>
          <span class="set-row-main"><b>数据导出 / 导入</b><small>JSON / CSV / Excel，跨设备迁移</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="打开回收站" @click="recycleOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M5 6l1 14h12l1-14"/><path d="M10 11v6M14 11v6"/></svg></span>
          <span class="set-row-main"><b>回收站</b><small>近 7 天的操作，可撤销恢复</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
      </div>
    </section>

    <!-- 关于 -->
    <section class="set-group">
      <div class="set-group-head"><b>关于</b><span>版本与版权信息</span></div>
      <div class="card set-group-items">
        <button class="set-row" aria-label="查看版本信息与更新日志" @click="versionOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg></span>
          <span class="set-row-main"><b>版本信息</b><small>当前版本与更新日志</small></span>
          <span class="ver-tag mono">{{ versionLabel }}</span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
        <button class="set-row" aria-label="开源许可与版权声明" @click="licensesOpen = true">
          <span class="set-row-ico"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l8 4v5c0 5-3.4 8-8 9-4.6-1-8-4-8-9V7z"/><path d="M9 12l2 2 4-4"/></svg></span>
          <span class="set-row-main"><b>开源许可与版权声明</b><small>第三方组件归属与许可证全文</small></span>
          <svg class="set-row-arrow" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
        </button>
      </div>
    </section>

    <!-- ============ 二级窗口 ============ -->
    <!-- 外观与动效 -->
    <div v-if="appearanceOpen" class="modal-mask set-sheet" @click.self="appearanceOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="外观与动效">
        <div class="modal-head"><h3>外观与动效</h3><button class="x" @click="appearanceOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p class="muted set-sheet-desc">主题与动效偏好保存在本机，可跟随系统或手动固定。</p>
          <div class="set-field-label">主题</div>
          <div class="seg theme-seg">
            <button v-for="o in themeOptions" :key="o.v" :class="{ 'on-theme': themeMode === o.v }" @click="pickTheme(o.v, $event)">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="o.icon" /></svg>
              {{ o.t }}
            </button>
          </div>
          <div class="set-field-label spaced">动效</div>
          <div class="seg theme-seg">
            <button :class="{ 'on-theme': motionMode === 'system' }" @click="pickMotion('system')">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/></svg>
              跟随系统
            </button>
            <button :class="{ 'on-theme': motionMode === 'reduce' }" @click="pickMotion('reduce')">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h16"/><path d="M8 8l-4 4 4 4"/></svg>
              减少动效
            </button>
          </div>
          <div class="muted settings-note">当前：{{ themeLabel }} · {{ motionEffective === 'reduce' ? '减少动效' : '完整动效' }}<template v-if="motionMode === 'system' && sysReduce">（系统已开启）</template></div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="appearanceOpen = false">完成</button></div>
      </div>
    </div>

    <!-- 识别引擎 -->
    <div v-if="engineOpen" class="modal-mask set-sheet" @click.self="engineOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="识别引擎">
        <div class="modal-head"><h3>识别引擎</h3><button class="x" @click="engineOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p class="muted set-sheet-desc">决定「上传截图」时用哪种方式识别，随时可切换。</p>
          <div class="engine-cards">
            <div class="engine-card" :class="{ on: engine === 'local' }" @click="selectEngine('local')">
              <div class="ec-head"><span class="ec-title">本地 OCR</span><span class="badge badge-ok"><span class="dot"></span>默认 · 推荐</span></div>
              <div class="ec-desc">浏览器内 WASM 离线运行，简体中文模型已随程序内嵌。<b>无需 API Key、无需联网、图片不上传</b>，开箱即用。</div>
              <div class="ec-foot">识别后由规则解析金额/单号/时间，字段可在表单里核对修改</div>
            </div>
            <div class="engine-card" :class="{ on: engine === 'vision' }" @click="selectEngine('vision')">
              <div class="ec-head"><span class="ec-title">视觉大模型</span><span class="badge badge-buy"><span class="dot"></span>进阶</span></div>
              <div class="ec-desc">调用 OpenAI 兼容的视觉模型，对复杂排版、商品标题的<b>理解更准</b>，需自行配置 API Key。</div>
              <div class="ec-foot">图片会发送到你配置的接口，请在启用前确认该服务商的数据条款</div>
            </div>
          </div>
          <div v-if="msg" class="tip" :class="{ err: !msg.ok }">{{ msg.text }}</div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="engineOpen = false">完成</button></div>
      </div>
    </div>

    <!-- 视觉大模型 API -->
    <div v-if="advancedOpen" class="modal-mask set-sheet" @click.self="advancedOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="视觉大模型 API">
        <div class="modal-head"><h3>视觉大模型 API</h3><button class="x" @click="advancedOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p class="muted set-sheet-desc">仅当使用「视觉大模型」引擎时需要；本地 OCR 用户可完全忽略。</p>
          <div class="field">
            <label>服务商预设<HelpTip text="点一下自动填端点与模型" /></label>
            <div class="preset-row">
              <button v-for="p in presets" :key="p.name" class="btn btn-sm" :class="{ on: form.ocr_api_base === p.base && form.ocr_model === p.model }" @click="applyPreset(p)">{{ p.name }}</button>
            </div>
          </div>
          <div class="field"><label>API 地址（OpenAI 兼容 /v1）</label><input class="input mono" v-model="form.ocr_api_base" placeholder="https://api.openai.com/v1" /></div>
          <div class="field"><label>视觉模型名称<HelpTip text="需选择支持图片输入的模型，否则识别会失败" /></label><input class="input mono" v-model="form.ocr_model" placeholder="gpt-4o-mini" /></div>
          <div class="field">
            <label>API Key<HelpTip text="Key 仅保存在本机 xianyu.db，不会回显明文。" /></label>
            <input class="input mono" type="password" v-model="form.ocr_api_key" autocomplete="new-password" :placeholder="meta.has_key ? `已保存 ${meta.masked_key}，留空表示不修改` : 'sk-...'" />
            <div v-if="meta.source === 'env' && meta.env_fallback" class="hint-inline" style="margin-top:4px">当前使用环境变量中的配置。</div>
          </div>
        </div>
        <div class="modal-foot ocr-api-foot">
          <button class="btn btn-quiet" @click="test" :disabled="testing || saving"><span v-if="testing" class="spin"></span>测试连接</button>
          <button class="btn btn-ghost btn-danger" @click="clearKey" :disabled="!meta.has_key">清除 Key</button>
          <div class="spacer"></div>
          <button class="btn btn-primary" @click="save" :disabled="saving"><span v-if="saving" class="spin"></span>保存配置</button>
        </div>
      </div>
    </div>

    <!-- 识别与配对说明 -->
    <div v-if="helpOpen" class="modal-mask set-sheet" @click.self="helpOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="识别与配对说明">
        <div class="modal-head"><h3>识别与配对说明</h3><button class="x" @click="helpOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body help-body">
          <div class="help-row"><b>买进单判定</b><span>截图出现「卖家昵称」→ 我是买家 → 识别为买进侧</span></div>
          <div class="help-row"><b>卖出单判定</b><span>截图出现「买家昵称」/「预计到手」→ 我是卖家 → 识别为卖出侧</span></div>
          <div class="help-row"><b>自动配对</b><span>按商品名把买进侧与卖出侧合并成一条记录；同名多件货依次配对，可用「两侧一起填」或编辑调整</span></div>
          <div class="help-row"><b>利润口径</b><span>净利润 = 成交额 − 买价 − 买运费 − 卖运费 − 软件服务费 − 无忧卖服务费。「到手价」= 成交价 − 服务费 − 无忧卖 − 卖运费，用于展示与对账，不参与净利润计算</span></div>
          <div class="help-row"><b>同步口径</b><span>闲鱼订单列表只覆盖近 3 个月（接口固定回 40 单）；历史订单在「闲鱼同步」里把「账单基准回溯」调到 6/12/24 个月，按收入 / 支出账单补建</span></div>
          <div class="help-row"><b>手动录入</b><span>不识别也完全可用，直接在弹窗里填写买进/卖出两侧即可</span></div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="helpOpen = false">知道了</button></div>
      </div>
    </div>

    <!-- 访问密码 -->
    <div v-if="pwdOpen" class="modal-mask set-sheet" @click.self="pwdOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="访问密码">
        <div class="modal-head"><h3>访问密码</h3><button class="x" @click="pwdOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <div class="notice-box">开启后，所有数据接口都需要登录；登录状态保存在 HttpOnly Cookie 中，7 天有效。密码以加盐哈希存储，不保存明文。<b>忘记密码</b>可在服务器执行 <code>xianyu-stat --reset-password</code> 清除。</div>
          <div class="form-grid tight">
            <div class="field" v-if="auth.enabled"><label>当前密码</label><input class="input" type="password" v-model="pwdForm.old" autocomplete="current-password" placeholder="修改或关闭时需要" /></div>
            <div class="field"><label>新密码</label><input class="input" type="password" v-model="pwdForm.password" autocomplete="new-password" placeholder="至少 8 位（建议字母 + 数字）" /></div>
            <div class="field"><label>确认新密码</label><input class="input" type="password" v-model="pwdForm.password2" autocomplete="new-password" placeholder="再输入一次" /></div>
          </div>
          <div class="muted settings-note">管理密码至少 {{ MIN_ADMIN_PWD }} 位（建议字母 + 数字）；只读密码单独设置，至少 {{ MIN_READONLY_PWD }} 位。两者互不影响。</div>
          <div v-if="pwdMsg" class="tip" :class="{ err: !pwdMsg.ok }">{{ pwdMsg.text }}</div>
        </div>
        <div class="modal-foot">
          <button class="btn btn-primary" @click="savePassword" :disabled="pwdBusy"><span v-if="pwdBusy" class="spin"></span>{{ auth.enabled ? '修改密码' : '开启访问密码' }}</button>
          <button v-if="auth.enabled" class="btn btn-ghost btn-danger" @click="disablePassword" :disabled="pwdBusy">关闭密码</button>
        </div>
      </div>
    </div>

    <!-- 自动备份 -->
    <div v-if="backupOpen" class="modal-mask set-sheet" @click.self="backupOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="自动备份">
        <div class="modal-head"><h3>自动备份</h3><button class="x" @click="backupOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p class="muted set-sheet-desc">定时生成数据库一致性快照，导入或覆盖数据前也会自动备份。</p>
          <div class="backup-config">
            <label class="switch-row"><input type="checkbox" v-model="backup.enabled" /><span>启用定时自动备份</span></label>
            <div class="cfg-item"><label>备份周期（小时）</label><input class="input num" type="number" min="1" max="720" v-model.number="backup.interval_hours" /></div>
            <div class="cfg-item"><label>保留份数</label><input class="input num" type="number" min="1" max="90" v-model.number="backup.keep" /></div>
            <div class="set-actions">
              <button class="btn btn-sm" @click="saveBackupConfig" :disabled="backupBusy">保存设置</button>
              <button class="btn btn-sm btn-quiet" @click="verifyBackupNow" :disabled="verifyBusy"><span v-if="verifyBusy" class="spin"></span>{{ verifyBusy ? '对账中…' : '对账' }}</button>
              <button class="btn btn-sm btn-primary" @click="runBackupNow" :disabled="backupBusy"><span v-if="backupBusy" class="spin"></span>立即备份</button>
            </div>
          </div>
          <div class="muted settings-note">备份目录：<code class="mono">{{ backup.dir }}</code> · 共 {{ backup.files.length }} 份</div>
          <div v-if="backupError" class="tip err" role="alert">
            <span>备份状态没读出来：{{ backupError }}</span>
            <button class="btn btn-sm" @click="loadBackups">重试</button>
          </div>
          <div v-if="backupMsg" class="tip" :class="{ err: !backupMsg.ok }">{{ backupMsg.text }}</div>
          <div class="lic-table" v-if="backup.files.length">
            <div class="lic-head back-head"><span>文件名</span><span>大小</span><span>时间</span><span>操作</span></div>
            <div v-for="f in backup.files" :key="f.name" class="lic-row back-row">
              <span class="lic-name mono" :title="f.name">{{ f.name }}</span>
              <span class="muted num">{{ fmtSize(f.size) }}</span>
              <span class="muted">{{ f.created }}</span>
              <span><a class="link" :href="api.backupUrl(f.name)" :download="f.name">下载</a><a class="link" style="margin-left:10px;color:var(--up)" @click="deleteBackup(f.name)">删除</a></span>
            </div>
          </div>
          <div v-else-if="!backupError" class="empty empty-compact">
            <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/></svg></div>
            <div class="empty-title">暂无备份文件</div>
            <div class="empty-sub">点「立即备份」生成第一份完整快照</div>
          </div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="backupOpen = false">完成</button></div>
      </div>
    </div>

    <!-- 数据导出 / 导入 -->
    <div v-if="dataOpen" class="modal-mask set-sheet" @click.self="dataOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="数据导出与导入">
        <div class="modal-head"><h3>数据导出 / 导入</h3><button class="x" @click="dataOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <div class="notice-box">JSON / CSV / Excel <b>只包含当前店铺</b>的明细（另含全局的运费标签与识别设置；<b>不含访问密码与 API Key</b>），可用于系统间迁移；导入同样只写入当前店铺。导入前会自动备份当前数据。</div>
          <div class="set-field-label">导出</div>
          <div class="data-actions">
            <button class="btn btn-primary" :disabled="!!exporting" @click="doExport('json')"><span v-if="exporting === 'json'" class="spin"></span>{{ exporting === 'json' ? '导出中' : 'JSON 完整备份' }}</button>
            <button class="btn btn-quiet" :disabled="!!exporting" @click="doExport('csv')"><span v-if="exporting === 'csv'" class="spin"></span>{{ exporting === 'csv' ? '导出中' : 'CSV 表格' }}</button>
            <button class="btn btn-quiet" @click="doExportExcel" :disabled="excelBusy">Excel 表格</button>
          </div>
          <div class="divider"></div>
          <div class="set-field-label">导入</div>
          <div class="import-row">
            <div class="seg seg-sm import-mode-seg">
              <button :class="{ 'on-both': importMode === 'merge' }" :aria-pressed="importMode === 'merge'" :disabled="importBusy" @click="importMode = 'merge'">追加到现有数据</button>
              <button :class="{ 'on-sell': importMode === 'replace' }" :aria-pressed="importMode === 'replace'" :disabled="importBusy" @click="importMode = 'replace'">覆盖当前店铺数据</button>
            </div>
            <button class="btn btn-quiet" @click="pickFile" :disabled="importBusy"><span v-if="importBusy" class="spin"></span>选择 JSON 文件并导入</button>
            <button class="btn btn-quiet" @click="pickCsv" :disabled="importBusy"><span v-if="importBusy" class="spin"></span>导入 CSV 表格</button>
            <input ref="fileInput" type="file" accept=".json,application/json" hidden @change="onImportFile" />
            <input ref="csvInput" type="file" accept=".csv,text/csv" hidden @change="onCsvFile" />
          </div>
          <!-- CSV 表头映射预览：导入前先看清「哪列进哪个字段、哪列会被丢掉」 -->
          <div v-if="csvPreview" class="csv-preview">
            <div class="csv-preview-head">
              <b>CSV 表头识别</b>
              <span class="muted">识别 {{ csvPreview.mapped }} 列 · 共 {{ csvPreview.count }} 条数据</span>
            </div>
            <div class="csv-map">
              <span v-for="c in csvPreview.cols" :key="c.idx" class="csv-map-item" :class="{ bad: !c.field }">
                <b>{{ c.name || '（空表头）' }}</b>
                <i v-if="c.field">→ {{ c.label }}</i>
                <i v-else>未识别，不会导入</i>
              </span>
            </div>
            <div v-if="csvPreview.rows.length" class="csv-sample">
              <table>
                <thead><tr><th v-for="h in csvPreview.head" :key="h">{{ h }}</th></tr></thead>
                <tbody>
                  <tr v-for="(r, i) in csvPreview.rows" :key="i"><td v-for="(v, j) in r" :key="j">{{ v || '—' }}</td></tr>
                </tbody>
              </table>
            </div>
            <div v-if="csvPreview.ignored.length" class="csv-warn">
              {{ csvPreview.ignored.length }} 列不会被导入：{{ csvPreview.ignored.join('、') }}
            </div>
          </div>
          <div v-if="dataMsg" class="tip" :class="{ err: !dataMsg.ok }">{{ dataMsg.text }}</div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="dataOpen = false">完成</button></div>
      </div>
    </div>

    <!-- 月度利润目标 -->
    <div v-if="targetOpen" class="modal-mask set-sheet" @click.self="targetOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="月度利润目标">
        <div class="modal-head"><h3>月度利润目标</h3><button class="x" @click="targetOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p class="muted set-sheet-desc">设一个当月净利润目标，首页会显示完成进度和剩余天数。按店铺、按月份分别保存；填 0 表示不设目标。</p>
          <div class="target-row">
            <input class="input num" type="month" v-model="targetMonth" :disabled="targetSaving" aria-label="目标月份" />
            <input class="input num" type="number" min="0" step="0.01" v-model.number="targetValue" :disabled="targetSaving || targetLoading || targetLoadedMonth !== targetMonth || !targetMonth" placeholder="目标净利润，如 5000" aria-label="目标金额" />
          </div>
          <div v-if="targetLoading" class="tip" role="status">正在读取该月目标…</div>
          <div v-else-if="targetReadError" class="tip err" role="alert">{{ targetReadError }} <button class="btn btn-sm" @click="loadTarget">重试</button></div>
          <div v-else-if="targetMsg" class="tip" :class="{ err: !targetMsg.ok }" :role="targetMsg.ok ? 'status' : 'alert'">{{ targetMsg.text }}</div>
        </div>
        <div class="modal-foot"><button class="btn btn-ghost" @click="targetOpen = false">关闭</button><button class="btn btn-primary" :disabled="!canSaveTarget" @click="saveTarget"><span v-if="targetSaving" class="spin"></span>{{ targetSaving ? '保存中…' : '保存' }}</button></div>
      </div>
    </div>

    <!-- 只读账号 -->
    <div v-if="readonlyOpen" class="modal-mask set-sheet" @click.self="readonlyOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="只读账号">
        <div class="modal-head"><h3>只读账号</h3><button class="x" @click="readonlyOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <!-- 没有管理密码时，只读限制没有主语：先拦在这里，并给出一步直达的入口 -->
          <div v-if="!auth.enabled" class="notice-box readonly-lock" role="status">
            <b>还不能设置只读密码</b>
            <div>只读账号靠管理密码来区分权限。现在系统没有管理密码，任何能访问这个地址的人都能直接改数据，设只读密码也不会真正限制谁。</div>
            <div>请先开启「访问密码」（即管理密码），再回来设置只读账号。</div>
          </div>
          <p class="muted set-sheet-desc">设一个独立密码，用它的登录只能查看数据，不能新增、修改或删除。主密码仍拥有完整权限。</p>
          <div class="readonly-row">
            <input class="input" type="password" v-model="readonlyPwd"
                   :disabled="!auth.enabled" :placeholder="auth.enabled ? `设置只读密码（至少 ${MIN_READONLY_PWD} 位）` : '请先设置管理密码'"
                   autocomplete="new-password" />
          </div>
          <p class="muted set-sheet-desc" v-if="meta.has_readonly">当前状态：已开启</p>
        </div>
        <div class="modal-foot">
          <template v-if="!auth.enabled">
            <button class="btn btn-ghost" @click="readonlyOpen = false">关闭</button>
            <button class="btn btn-primary" @click="readonlyOpen = false; pwdOpen = true">去设置管理密码</button>
          </template>
          <template v-else>
            <button v-if="meta.has_readonly" class="btn btn-ghost btn-danger" @click="clearReadonly">清除只读密码</button>
            <button class="btn btn-primary" @click="saveReadonly" :disabled="!readonlyPwd">保存</button>
          </template>
        </div>
      </div>
    </div>

    <!-- 开源许可 -->
    <div v-if="licensesOpen" class="modal-mask set-sheet" @click.self="licensesOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="开源许可与版权声明">
        <div class="modal-head"><h3>开源许可与版权声明</h3><button class="x" @click="licensesOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <div class="notice-box">本程序为个人本地记账工具，界面与业务逻辑为原创实现。<b>未包含</b>闲鱼的任何商标、图标、截图或界面素材；用户上传的订单截图仅在本机处理，不随程序分发。「闲鱼」为阿里巴巴集团旗下商标，此处仅作描述性用途。</div>
          <div class="lic-table">
            <div class="lic-head"><span>类型</span><span>组件</span><span>版本</span><span>许可证</span><span>原文</span></div>
            <div v-for="(l, i) in licenses" :key="i" class="lic-row">
              <span class="muted">{{ l[0] }}</span><span class="lic-name" :title="l[1]">{{ l[1] }}</span>
              <span class="mono muted">{{ l[2] }}</span><span><span class="badge badge-lic">{{ l[3] }}</span></span>
              <span><a v-if="l[4]" class="link" :href="'/' + l[4]" target="_blank" rel="noopener">查看</a><span v-else class="muted">—</span></span>
            </div>
          </div>
          <div class="muted settings-note">共 {{ licenses.length }} 项。完整声明亦见 THIRD_PARTY_NOTICES.md。</div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="licensesOpen = false">完成</button></div>
      </div>
    </div>

    <!-- 版本信息：独立组件自带弹窗 -->
    <VersionPanel v-if="versionOpen" @close="versionOpen = false" />

    <!-- 其余二级窗口：各自带遮罩 -->
    <ShopManage v-if="shopOpen" @close="shopOpen = false" @notify="(t) => emit('notify', t)" />
    <GoofishSync v-if="gfOpen" @close="gfOpen = false" @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />
    <AlipayImport v-if="aliOpen" @close="aliOpen = false" @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />
    <RecyclePanel v-if="recycleOpen" @close="recycleOpen = false" @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />
  </div>
</template>
