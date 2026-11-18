<script setup>
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { api, downloadFile } from '../api'
import { confirm } from '../confirm'
import { themeMode, motionMode } from '../theme'
import RecyclePanel from '../components/RecyclePanel.vue'
import VersionPanel from '../components/VersionPanel.vue'
import ShopManage from '../components/ShopManage.vue'
import GoofishSync from '../components/GoofishSync.vue'
import AlipayImport from '../components/AlipayImport.vue'
import HelpTip from '../components/HelpTip.vue'
import { undoAction } from '../feedback'

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
function pickTheme(v) {
  if (themeMode.value === v) {
    emit('notify', `当前已经是「${themeLabel.value}」`, false, null, { tone: 'info' })
    return
  }
  themeMode.value = v
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
const targetMonth = ref(new Date().toISOString().slice(0, 7))
const targetValue = ref(0)
const targetSaving = ref(false)
const targetMsg = ref('')
async function loadTarget() {
  try {
    const r = await api.getMonthlyTarget(targetMonth.value)
    targetValue.value = Number(r?.target) || 0
  } catch (e) { targetValue.value = 0 }
}
async function saveTarget() {
  targetSaving.value = true; targetMsg.value = ''
  try {
    await api.saveMonthlyTarget(targetMonth.value, Number(targetValue.value) || 0)
    targetMsg.value = '已保存'
    emit('refresh')
    setTimeout(() => { targetMsg.value = '' }, 2500)
  } catch (e) { targetMsg.value = e.message || '保存失败' }
  finally { targetSaving.value = false }
}
onMounted(loadTarget)
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
const backupMsg = ref(null)

// ---------- 数据导入导出 ----------
const dataOpen = ref(false)
const importMode = ref('merge')
const importBusy = ref(false)
const dataMsg = ref(null)
const excelBusy = ref(false)
const exporting = ref('')   // '' | 'json' | 'csv'：导出期间的按钮忙碌态
const fileInput = ref(null)
const readonlyPwd = ref('')

async function saveReadonly() {
  if (readonlyPwd.value.length < 4) { emit('notify', '只读密码至少 4 位', true); return }
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
  try {
    const r = await api.verifyBackup()
    backupMsg.value = {
      text: r.consistent
        ? `对账一致：数据库 ${r.items_in_db} 条 / 最新备份 ${r.items_in_backup} 条`
        : `对账不一致：数据库 ${r.items_in_db} 条 / 备份 ${r.items_in_backup === null ? '无可用备份' : r.items_in_backup + ' 条'}`,
      ok: !!r.consistent
    }
  } catch (e) { backupMsg.value = { text: '对账失败：' + e.message, ok: false } }
}

async function loadAuth() {
  try { auth.value = await api.authStatus() } catch (e) { /* ignore */ }
}
async function loadBackups() {
  try { backup.value = await api.getBackups() } catch (e) { /* ignore */ }
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
onMounted(() => { load(); loadLicenses(); loadAuth(); loadBackups() })
// 撤销覆盖导入等整库级操作后，由 App 提升 refreshKey 触发本页重载
watch(() => props.refreshKey, () => { load(); loadBackups(); loadAuth() })

// ---------- 访问密码 ----------
async function savePassword() {
  pwdMsg.value = null
  if (!pwdForm.password) { pwdMsg.value = { text: '请输入新密码', ok: false }; return }
  if (pwdForm.password !== pwdForm.password2) { pwdMsg.value = { text: '两次输入的新密码不一致', ok: false }; return }
  if (pwdForm.password.length < 4) { pwdMsg.value = { text: '密码至少 4 位', ok: false }; return }
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
    pwdMsg.value = { text: '访问密码已关闭', ok: true }
    emit('notify', '访问密码已关闭')
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
function pickFile() { fileInput.value?.click() }
async function onImportFile(e) {
  const f = e.target.files?.[0]
  if (!f) return
  dataMsg.value = null
  let parsed
  try {
    parsed = JSON.parse(await f.text())
  } catch (err) {
    dataMsg.value = { text: '文件不是有效的 JSON', ok: false }
    e.target.value = ''; return
  }
  if (!parsed || !Array.isArray(parsed.items)) {
    dataMsg.value = { text: '缺少 items 数据，不是本系统导出的文件', ok: false }
    e.target.value = ''; return
  }
  // 前端先做一遍数据体检，避免把坏数据送进后端再触发事务回滚
  const seenOrders = new Set()
  for (const it of parsed.items) {
    const no = String(it.sell_order_no || it.buy_order_no || '').trim()
    if (no && seenOrders.has(no)) {
      dataMsg.value = { text: `导入文件存在重复订单号 ${no}`, ok: false }
      e.target.value = ''; return
    }
    if (no) seenOrders.add(no)
    for (const k of ['buy_price', 'sell_price', 'sell_net', 'buy_shipping', 'sell_shipping']) {
      if (Number(it[k]) < 0) {
        dataMsg.value = { text: `订单 ${no || '—'} 的 ${k} 为负数`, ok: false }
        e.target.value = ''; return
      }
    }
    const t = String(it.buy_time || it.sell_time || '').trim()
    if (t && !/^\d{4}-\d{2}-\d{2}/.test(t)) {
      dataMsg.value = { text: `订单 ${no || '—'} 的时间格式不合法：${t}`, ok: false }
      e.target.value = ''; return
    }
  }
  const modeText = importMode.value === 'replace' ? '覆盖（清空现有数据）' : '追加'
  const ok = await confirm({
    title: '导入数据？',
    message: `将以「${modeText}」方式导入 ${parsed.items.length} 条记录。`,
    detail: [
      importMode.value === 'replace' ? '覆盖模式会先清空现有全部数据。' : '追加模式会保留现有记录。',
      '导入前系统会自动生成一份备份；覆盖导入后也可在提示条中撤销（15 秒）。'
    ],
    confirmText: '确认导入', danger: true
  })
  if (!ok) { e.target.value = ''; return }
  importBusy.value = true
  try {
    const r = await api.importData(importMode.value, parsed)
    dataMsg.value = { text: r.message, ok: true }
    emit('notify', '数据导入完成', false, undoAction(r.undo_token, {
      label: '撤销导入',
      ttl: 15000,
      doneText: '已撤销导入，数据恢复到导入前',
      after: async () => { await loadBackups(); emit('refresh') },
      notify: (t) => emit('notify', t)
    }))
    await loadBackups()
  } catch (err) {
    dataMsg.value = { text: '导入失败：' + err.message, ok: false }
  } finally {
    importBusy.value = false
    e.target.value = ''
  }
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
    <!-- 外观 -->
    <div class="card settings-card">
      <div class="list-head">
        <div>
          <h3 class="section-title">外观<HelpTip text="跟随系统或手动固定，设置保存在本机" /></h3>
        </div>
      </div>
      <div class="settings-body">
        <div class="theme-row">
          <div class="seg theme-seg">
            <button v-for="o in themeOptions" :key="o.v" :class="{ 'on-theme': themeMode === o.v }" @click="pickTheme(o.v)">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path :d="o.icon" /></svg>
              {{ o.t }}
            </button>
          </div>
          <span class="muted theme-hint">当前：{{ themeLabel }}</span>
        </div>
        <div class="theme-row motion-row">
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
          <span class="muted theme-hint">
            当前：{{ motionEffective === 'reduce' ? '减少动效' : '完整动效' }}<template v-if="motionMode === 'system' && sysReduce">（系统已开启减少动态效果）</template>
          </span>
        </div>
      </div>
    </div>

    <!-- 月度利润目标 -->
    <div class="card settings-card">
      <div class="list-head">
        <div>
          <h3 class="section-title">月度利润目标<HelpTip text="给当前店铺设一个当月净利润目标，首页 Hero 会显示完成进度和本月剩余天数。按店铺、按月份分别保存，填 0 表示不设目标。" /></h3>
        </div>
      </div>
      <div class="settings-body">
        <div class="target-row">
          <input class="input num" type="month" v-model="targetMonth" aria-label="目标月份" />
          <input class="input num" type="number" min="0" step="100" v-model.number="targetValue" placeholder="目标净利润，如 5000" aria-label="目标金额" />
          <button class="btn btn-primary" :disabled="targetSaving" @click="saveTarget">
            <span v-if="targetSaving" class="spin"></span>保存
          </button>
        </div>
        <div v-if="targetMsg" class="tip" :class="{ err: targetMsg.includes('失败') }">{{ targetMsg }}</div>
      </div>
    </div>

    <!-- 识别引擎 -->
    <div class="card settings-card">
      <div class="list-head">
        <div>
          <h3 class="section-title">识别引擎<HelpTip text="决定「上传截图」时用哪种方式识别，随时可切换并保存在本机" /></h3>
        </div>
      </div>
      <div class="settings-body">
        <div class="engine-cards">
          <div class="engine-card" :class="{ on: engine === 'local' }" @click="selectEngine('local')">
            <div class="ec-head">
              <span class="ec-title">本地 OCR</span>
              <span class="badge badge-ok"><span class="dot"></span>默认 · 推荐</span>
            </div>
            <div class="ec-desc">浏览器内 WASM 离线运行，简体中文模型已随程序内嵌。<b>无需 API Key、无需联网、图片不上传</b>，开箱即用。</div>
            <div class="ec-foot">识别后由规则解析金额/单号/时间，字段可在表单里核对修改</div>
          </div>
          <div class="engine-card" :class="{ on: engine === 'vision' }" @click="selectEngine('vision')">
            <div class="ec-head">
              <span class="ec-title">视觉大模型</span>
              <span class="badge badge-buy"><span class="dot"></span>进阶</span>
            </div>
            <div class="ec-desc">调用 OpenAI 兼容的视觉模型，对复杂排版、商品标题的<b>理解更准</b>，需自行配置 API Key。</div>
            <div class="ec-foot">图片会发送到你配置的接口，请在启用前确认该服务商的数据条款</div>
          </div>
        </div>
        <div v-if="msg" class="tip" :class="{ err: !msg.ok }">{{ msg.text }}</div>
      </div>
    </div>

    <!-- 进阶设置：视觉大模型 -->
    <div class="card settings-card">
      <div class="list-head adv-head" @click="advancedOpen = !advancedOpen">
        <div>
          <h3 class="section-title">进阶设置 · 视觉大模型 API<HelpTip text="仅当使用「视觉大模型」引擎时需要；本地 OCR 用户可完全忽略" /></h3>
        </div>
        <span class="badge" :class="meta.has_key ? 'badge-ok' : 'badge-buy'">
          <span class="dot"></span>{{ meta.has_key ? '已配置 Key' : '未配置 Key' }}
        </span>
        <span class="adv-arrow" :class="{ open: advancedOpen }">{{ advancedOpen ? '收起' : '展开' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></span>
      </div>

      <div v-if="advancedOpen" class="settings-body">
        <div class="field">
          <label>服务商预设<HelpTip text="点一下自动填端点与模型" /></label>
          <div class="preset-row">
            <button v-for="p in presets" :key="p.name" class="btn btn-sm" :class="{ on: form.ocr_api_base === p.base && form.ocr_model === p.model }" @click="applyPreset(p)">{{ p.name }}</button>
          </div>
        </div>
        <div class="field">
          <label>API 地址（OpenAI 兼容 /v1）</label>
          <input class="input mono" v-model="form.ocr_api_base" placeholder="https://api.openai.com/v1" />
        </div>
        <div class="field">
          <label>视觉模型名称<HelpTip text="需选择支持图片输入的模型，否则识别会失败" /></label>
          <input class="input mono" v-model="form.ocr_model" placeholder="gpt-4o-mini" />
        </div>
        <div class="field">
          <label>API Key<HelpTip text="Key 仅保存在本机 xianyu.db，不会回显明文。" /></label>
          <input class="input mono" type="password" v-model="form.ocr_api_key" autocomplete="new-password"
            :placeholder="meta.has_key ? `已保存 ${meta.masked_key}，留空表示不修改` : 'sk-...'" />
          <div v-if="meta.source === 'env' && meta.env_fallback" class="hint-inline" style="margin-top:4px">当前使用环境变量中的配置。</div>
        </div>
        <div class="settings-actions">
          <button class="btn btn-quiet" @click="test" :disabled="testing || saving">
            <span v-if="testing" class="spin"></span>测试连接
          </button>
          <button class="btn btn-ghost btn-danger" @click="clearKey" :disabled="!meta.has_key">清除 Key</button>
          <div class="spacer"></div>
          <button class="btn btn-primary" @click="save" :disabled="saving">
            <span v-if="saving" class="spin"></span>保存配置
          </button>
        </div>
      </div>
    </div>

    <!-- 访问密码 -->
    <div class="card settings-card">
      <div class="list-head adv-head" @click="pwdOpen = !pwdOpen">
        <div>
          <h3 class="section-title">访问密码<HelpTip text="部署在局域网/公网时建议开启，保护你的交易数据" /></h3>
        </div>
        <span class="badge" :class="auth.enabled ? 'badge-ok' : 'badge-buy'">
          <span class="dot"></span>{{ auth.enabled ? '已开启' : '未开启' }}
        </span>
        <span class="adv-arrow" :class="{ open: pwdOpen }">{{ pwdOpen ? '收起' : '展开' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></span>
      </div>
      <div v-if="pwdOpen" class="settings-body">
        <div class="notice-box">
          开启后，所有数据接口都需要登录；登录状态保存在 HttpOnly Cookie 中，7 天有效。密码以加盐哈希存储，不保存明文。<b>忘记密码</b>可在服务器执行 <code>xianyu-stat --reset-password</code> 清除。
        </div>
        <div class="form-grid tight">
          <div class="field" v-if="auth.enabled">
            <label>当前密码</label>
            <input class="input" type="password" v-model="pwdForm.old" autocomplete="current-password" placeholder="修改或关闭时需要" />
          </div>
          <div class="field">
            <label>新密码</label>
            <input class="input" type="password" v-model="pwdForm.password" autocomplete="new-password" placeholder="至少 4 位" />
          </div>
          <div class="field">
            <label>确认新密码</label>
            <input class="input" type="password" v-model="pwdForm.password2" autocomplete="new-password" placeholder="再输入一次" />
          </div>
        </div>
        <div v-if="pwdMsg" class="tip" :class="{ err: !pwdMsg.ok }">{{ pwdMsg.text }}</div>
        <div class="settings-actions">
          <button class="btn btn-primary" @click="savePassword" :disabled="pwdBusy">
            <span v-if="pwdBusy" class="spin"></span>{{ auth.enabled ? '修改密码' : '开启访问密码' }}
          </button>
          <button v-if="auth.enabled" class="btn btn-ghost btn-danger" @click="disablePassword" :disabled="pwdBusy">关闭密码</button>
        </div>
      </div>
    </div>

    <!-- 自动备份 -->
    <div class="card settings-card">
      <div class="list-head adv-head" @click="backupOpen = !backupOpen">
        <div>
          <h3 class="section-title">自动备份<HelpTip text="定时生成数据库一致性快照，导入或覆盖数据前也会自动备份" /></h3>
        </div>
        <span class="badge" :class="backup.enabled ? 'badge-ok' : 'badge-buy'">
          <span class="dot"></span>{{ backup.enabled ? '已开启' : '未开启' }}
        </span>
        <span class="adv-arrow" :class="{ open: backupOpen }">{{ backupOpen ? '收起' : '展开' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></span>
      </div>
      <div v-if="backupOpen" class="settings-body">
        <div class="backup-config">
          <label class="switch-row">
            <input type="checkbox" v-model="backup.enabled" />
            <span>启用定时自动备份</span>
          </label>
          <div class="cfg-item">
            <label>备份周期（小时）</label>
            <input class="input num" type="number" min="1" max="720" v-model.number="backup.interval_hours" />
          </div>
          <div class="cfg-item">
            <label>保留份数</label>
            <input class="input num" type="number" min="1" max="90" v-model.number="backup.keep" />
          </div>
          <button class="btn btn-sm" @click="saveBackupConfig" :disabled="backupBusy">保存设置</button>
          <button class="btn btn-sm btn-quiet" @click="verifyBackupNow" title="对比数据库与最新备份的记录数">对账</button>
          <button class="btn btn-sm btn-primary" @click="runBackupNow" :disabled="backupBusy">
            <span v-if="backupBusy" class="spin"></span>立即备份
          </button>
        </div>
        <div class="muted settings-note">
          备份目录：<code class="mono">{{ backup.dir }}</code> · 上次备份：{{ backup.last || '从未' }} · 共 {{ backup.files.length }} 份
        </div>
        <div v-if="backupMsg" class="tip" :class="{ err: !backupMsg.ok }">{{ backupMsg.text }}</div>
        <div class="lic-table" v-if="backup.files.length">
          <div class="lic-head back-head"><span>文件名</span><span>大小</span><span>时间</span><span>操作</span></div>
          <div v-for="f in backup.files" :key="f.name" class="lic-row back-row">
            <span class="lic-name mono" :title="f.name">{{ f.name }}</span>
            <span class="muted num">{{ fmtSize(f.size) }}</span>
            <span class="muted">{{ f.created }}</span>
            <span>
              <a class="link" :href="api.backupUrl(f.name)" :download="f.name">下载</a>
              <a class="link" style="margin-left:10px;color:var(--up)" @click="deleteBackup(f.name)">删除</a>
            </span>
          </div>
        </div>
        <div v-else class="empty empty-compact">
          <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/></svg></div>
          <div class="empty-title">暂无备份文件</div>
          <div class="empty-sub">点「立即备份」生成第一份完整快照</div>
          <button class="btn btn-primary" @click="runBackupNow" :disabled="backupBusy">立即备份</button>
        </div>
      </div>
    </div>

    <!-- 导出 / 导入 -->
    <div class="card settings-card">
      <div class="list-head adv-head" @click="dataOpen = !dataOpen">
        <div>
          <h3 class="section-title">数据导出 / 导入<HelpTip text="在本机与 PVE 之间同步数据，或留存完整备份" /></h3>
        </div>
        <span class="adv-arrow" :class="{ open: dataOpen }">{{ dataOpen ? '收起' : '展开' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></span>
      </div>
      <div v-if="dataOpen" class="settings-body">
        <div class="notice-box">
          JSON / CSV / Excel <b>只包含当前店铺</b>的明细（另含全局的运费标签与识别设置；<b>不含访问密码与 API Key</b>），可用于系统间迁移；导入同样只写入当前店铺。导入前会自动备份当前数据。
        </div>
        <div class="data-actions">
          <button class="btn btn-primary" :disabled="!!exporting" @click="doExport('json')">
            <span v-if="exporting === 'json'" class="spin"></span>{{ exporting === 'json' ? '导出中' : '导出 JSON 完整备份' }}
          </button>
          <button class="btn btn-quiet" :disabled="!!exporting" @click="doExport('csv')">
            <span v-if="exporting === 'csv'" class="spin"></span>{{ exporting === 'csv' ? '导出中' : '导出 CSV 表格' }}
          </button>
          <button class="btn btn-quiet" @click="doExportExcel" :disabled="excelBusy">导出 Excel 表格</button>
          <HelpTip text="JSON：完整备份，可原样导回。&#10;Excel：带表头配色、冻结首行、筛选、两位小数与合计行，可直接打印对账。&#10;CSV：无格式纯文本，适合喂给其它系统。" />
        </div>

        <div class="divider"></div>
        <div class="import-row">
          <div class="seg seg-sm import-mode-seg">
            <button :class="{ 'on-both': importMode === 'merge' }" @click="importMode = 'merge'">追加到现有数据</button>
            <button :class="{ 'on-sell': importMode === 'replace' }" @click="importMode = 'replace'">覆盖全部数据</button>
          </div>
          <button class="btn btn-quiet" @click="pickFile" :disabled="importBusy">
            <span v-if="importBusy" class="spin"></span>选择 JSON 文件并导入
          </button>
          <input ref="fileInput" type="file" accept=".json,application/json" hidden @change="onImportFile" />
          <HelpTip text="「追加」保留现有记录并添加导入内容；「覆盖」先清空再导入（会自动备份）。&#10;导入的设置不会覆盖已有的密码与 API Key。" />
        </div>

        <div v-if="dataMsg" class="tip" :class="{ err: !dataMsg.ok }">{{ dataMsg.text }}</div>
      </div>
    </div>

    <!-- 闲鱼同步：绑定一次，之后一键拉单 + 自动对账 -->
    <div class="card settings-card settings-entry" role="button" tabindex="0"
         aria-label="打开闲鱼同步" @click="gfOpen = true"
         @keyup.enter="gfOpen = true" @keyup.space.prevent="gfOpen = true">
      <span class="se-icon" aria-hidden="true">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/></svg>
      </span>
      <div class="se-text">
        <h3 class="section-title">闲鱼同步<HelpTip text="绑定账号后一键拉取买卖订单，服务费与到手价按闲鱼账单自动核对" /></h3>
      </div>
      <span class="se-arrow" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
      </span>
    </div>

    <div class="card settings-card settings-entry" role="button" tabindex="0"
         aria-label="打开支付宝账单导入" @click="aliOpen = true"
         @keyup.enter="aliOpen = true" @keyup.space.prevent="aliOpen = true">
      <span class="se-icon" aria-hidden="true">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5-5 5 5"/><path d="M12 5v12"/></svg>
      </span>
      <div class="se-text">
        <h3 class="section-title">支付宝账单导入<HelpTip text="手续费按订单号、快递费按发货时间精确对上，只补费用不新增订单" /></h3>
      </div>
      <span class="se-arrow" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
      </span>
    </div>

    <!-- 回收站：收成一级入口，列表放进二级窗口 -->
    <div class="card settings-card settings-entry" role="button" tabindex="0"
         aria-label="管理店铺" @click="shopOpen = true"
         @keyup.enter="shopOpen = true" @keyup.space.prevent="shopOpen = true">
      <span class="se-icon" aria-hidden="true">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 9h16l-1 11H5L4 9z"/><path d="M8 9V6a4 4 0 0 1 8 0v3"/></svg>
      </span>
      <div class="se-text">
        <h3 class="section-title">店铺管理<HelpTip text="新建、改名、删除店铺；数据按店铺独立存放" /></h3>
      </div>
      <span class="se-arrow" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
      </span>
    </div>
    <ShopManage v-if="shopOpen" @close="shopOpen = false" @notify="(t) => emit('notify', t)" />
    <GoofishSync v-if="gfOpen" @close="gfOpen = false"
                 @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />
    <AlipayImport v-if="aliOpen" @close="aliOpen = false"
                  @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />

    <div class="card settings-card settings-entry" role="button" tabindex="0"
         aria-label="打开回收站" @click="recycleOpen = true"
         @keyup.enter="recycleOpen = true" @keyup.space.prevent="recycleOpen = true">
      <span class="se-icon" aria-hidden="true">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="M5 6l1 14h12l1-14"/><path d="M10 11v6M14 11v6"/></svg>
      </span>
      <div class="se-text">
        <h3 class="section-title">回收站<HelpTip text="最近 7 天的可撤销操作，随时可恢复" /></h3>
      </div>
      <span class="se-arrow" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
      </span>
    </div>

    <Transition name="modal">
      <RecyclePanel v-if="recycleOpen" @close="recycleOpen = false"
        @notify="(t) => emit('notify', t)" @refresh="emit('refresh')" />
    </Transition>

    <!-- 只读账号 -->
    <div class="card settings-card">
      <div class="list-head">
        <div>
          <h3 class="section-title">只读账号<HelpTip text="用独立密码登录时只能查看数据，不能新增、修改或删除" /></h3>
        </div>
        <span class="badge" :class="meta.has_readonly ? 'badge-ok' : 'badge-buy'"><span class="dot"></span>{{ meta.has_readonly ? '已开启' : '未设置' }}</span>
      </div>
      <div class="settings-body">
        <div class="readonly-row">
          <input class="input" type="password" v-model="readonlyPwd" placeholder="设置只读密码（至少 4 位）" autocomplete="new-password" />
          <button class="btn btn-primary" @click="saveReadonly" :disabled="!readonlyPwd">保存</button>
          <button v-if="meta.has_readonly" class="btn btn-quiet" @click="clearReadonly">清除</button>
          <HelpTip text="主密码仍拥有完整权限；已有登录会话按管理员处理，不受影响。" />
        </div>

      </div>
    </div>

    <!-- 使用说明 -->
    <div class="card settings-card">
      <div class="list-head"><h3 class="section-title">识别与配对说明</h3></div>
      <div class="settings-body help-body">
        <div class="help-row"><b>买进单判定</b><span>截图出现「卖家昵称」→ 我是买家 → 识别为买进侧</span></div>
        <div class="help-row"><b>卖出单判定</b><span>截图出现「买家昵称」/「预计到手」→ 我是卖家 → 识别为卖出侧</span></div>
        <div class="help-row"><b>自动配对</b><span>按商品名把买进侧与卖出侧合并成一条记录；同名多件货依次配对，可用「两侧一起填」或编辑调整</span></div>
        <div class="help-row"><b>利润口径</b><span>净利润 = 成交额 − 买价 − 买运费 − 卖运费 − 软件服务费 − 无忧卖服务费。「到手价」= 成交价 − 服务费 − 无忧卖 − 卖运费，用于展示与对账，不参与净利润计算</span></div>
        <div class="help-row"><b>同步口径</b><span>闲鱼订单列表只覆盖近 3 个月（接口固定回 40 单）；历史订单在「设置 → 闲鱼同步」里把「账单基准回溯」调到 6/12/24 个月，按收入 / 支出账单补建</span></div>
        <div class="help-row"><b>手动录入</b><span>不识别也完全可用，直接在弹窗里填写买进/卖出两侧即可</span></div>
      </div>
    </div>

    <!-- 开源许可 / 版权 -->
    <div class="card settings-card">
      <div class="list-head adv-head" @click="licensesOpen = !licensesOpen">
        <div>
          <h3 class="section-title">开源许可与版权声明<HelpTip text="本程序随二进制分发了开源组件，此处列出归属与许可证全文" /></h3>
        </div>
        <span class="adv-arrow" :class="{ open: licensesOpen }">{{ licensesOpen ? '收起' : '展开' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></span>
      </div>
      <div v-if="licensesOpen" class="settings-body">
        <div class="notice-box">
          本程序为个人本地记账工具，界面与业务逻辑为原创实现。<b>未包含</b>闲鱼的任何商标、图标、截图或界面素材；用户上传的订单截图仅在本机处理，不随程序分发。「闲鱼」为阿里巴巴集团旗下商标，此处仅作描述性用途。
        </div>
        <div class="lic-table">
          <div class="lic-head"><span>类型</span><span>组件</span><span>版本</span><span>许可证</span><span>原文</span></div>
          <div v-for="(l, i) in licenses" :key="i" class="lic-row">
            <span class="muted">{{ l[0] }}</span>
            <span class="lic-name" :title="l[1]">{{ l[1] }}</span>
            <span class="mono muted">{{ l[2] }}</span>
            <span><span class="badge badge-lic">{{ l[3] }}</span></span>
            <span><a v-if="l[4]" class="link" :href="'/' + l[4]" target="_blank" rel="noopener">查看</a><span v-else class="muted">—</span></span>
          </div>
        </div>
        <div class="muted settings-note">
          共 {{ licenses.length }} 项。完整声明亦见随程序附带的 THIRD_PARTY_NOTICES.md；Apache-2.0 与 MPL-2.0 全文可在
          <a class="link" href="/licenses/APACHE-2.0.txt" target="_blank" rel="noopener">Apache-2.0</a> /
          <a class="link" href="/licenses/MPL-2.0.txt" target="_blank" rel="noopener">MPL-2.0</a> 查阅。
        </div>
      </div>
    </div>

    <!-- 版本信息：入口 + 二级窗口 -->
    <div class="card settings-card settings-entry" role="button" tabindex="0"
         aria-label="查看版本信息与更新日志" @click="versionOpen = true"
         @keyup.enter="versionOpen = true" @keyup.space.prevent="versionOpen = true">
      <span class="se-icon" aria-hidden="true">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>
      </span>
      <div class="se-text">
        <h3 class="section-title">版本信息 <span class="ver-tag mono">{{ versionLabel }}</span><HelpTip text="查看当前版本、更新说明与历次编译记录" /></h3>
      </div>
      <span class="se-arrow" aria-hidden="true">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg>
      </span>
    </div>

    <Transition name="modal">
      <VersionPanel v-if="versionOpen" @close="versionOpen = false" />
    </Transition>
  </div>
</template>
