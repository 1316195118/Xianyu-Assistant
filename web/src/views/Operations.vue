<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { api } from '../api'
import { fmtMoney, fmtNum, shortTime } from '../utils'
import { switchShop } from '../shopStore'
import AlipayImport from '../components/AlipayImport.vue'

const props = defineProps({
  refreshKey: { type: Number, default: 0 },
  globalAdmin: { type: Boolean, default: false },
  initialTab: { type: String, default: 'health' }
})
const emit = defineEmits([
  'notify', 'goto-records', 'goto-record', 'goto-group', 'goto-consign',
  'goto-settings', 'use-price', 'refresh'
])

const tab = ref(props.initialTab || 'health')
const tabs = computed(() => {
  const list = [
    { key: 'health', label: '数据体检', icon: 'M4 20V10M10 20V4M16 20v-7M22 20H2' },
    { key: 'todo', label: '待办中心', icon: 'M9 11l3 3L22 4M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11' },
    { key: 'transport', label: '物流费用', icon: 'M3 7h11v10H3zM14 10h4l3 3v4h-7zM6 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM18 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4z' },
    { key: 'fees', label: '服务费核销', icon: 'M20 12V7a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h6M16 3v18M8 8h4M8 12h3' },
    { key: 'owners', label: '归属人对账', icon: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM19 8v6M22 11h-6' },
    { key: 'lots', label: '采购批次', icon: 'M21 8l-9-5-9 5 9 5 9-5zM3 8v8l9 5 9-5V8M12 13v8' },
    { key: 'pricing', label: '定价补货', icon: 'M12 2v20M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6' },
    { key: 'audit', label: '操作审计', icon: 'M12 8v5l3 2M21 12a9 9 0 1 1-3-6.7M21 3v6h-6' },
    { key: 'quality', label: '质量趋势', icon: 'M3 3v18h18M7 15l3-4 3 2 4-6' }
  ]
  if (props.globalAdmin) list.push({ key: 'multi', label: '多店驾驶舱', icon: 'M3 21V8l7-4 7 4v13M10 21v-5h4v5M3 13h4M17 13h4' })
  return list
})

const health = ref(null)
const transport = ref(null)
const fees = ref(null)
const ownerBook = ref(null)
const lots = ref(null)
const pricing = ref(null)
const multi = ref(null)
const todoCenter = ref(null)
const auditRows = ref([])
const qualityTrend = ref(null)
const loading = ref({})
const errors = ref({})
const alipayOpen = ref(false)
const auditQuery = ref('')
const auditLimit = ref(100)
const expandedAuditId = ref(0)

const tabLabel = computed(() => tabs.value.find((x) => x.key === tab.value)?.label || '经营中心')
const isBusy = computed(() => !!loading.value[tab.value])

async function loadHealth() {
  if (loading.value.health) return
  loading.value = { ...loading.value, health: true }
  errors.value = { ...errors.value, health: '' }
  try {
    health.value = await api.operationsHealth()
  } catch (e) {
    errors.value = { ...errors.value, health: e.message || '数据体检加载失败' }
  } finally {
    loading.value = { ...loading.value, health: false }
  }
}

async function loadTransport() {
  if (loading.value.transport) return
  loading.value = { ...loading.value, transport: true }
  errors.value = { ...errors.value, transport: '' }
  try {
    transport.value = await api.operationsTransport()
  } catch (e) {
    errors.value = { ...errors.value, transport: e.message || '物流费用加载失败' }
  } finally {
    loading.value = { ...loading.value, transport: false }
  }
}

async function loadFees() {
  if (loading.value.fees) return
  loading.value = { ...loading.value, fees: true }
  errors.value = { ...errors.value, fees: '' }
  try {
    fees.value = await api.operationsFees()
  } catch (e) {
    errors.value = { ...errors.value, fees: e.message || '服务费数据加载失败' }
  } finally {
    loading.value = { ...loading.value, fees: false }
  }
}

async function loadOwnerBook() {
  if (loading.value.owners) return
  loading.value = { ...loading.value, owners: true }
  errors.value = { ...errors.value, owners: '' }
  try {
    const data = await api.consignStats({})
    ownerBook.value = [
      ...(data.owners || []).map((x) => ({ ...x, kind: 'sell', kindLabel: '寄卖' })),
      ...(data.buyers || []).map((x) => ({ ...x, kind: 'buy', kindLabel: '代采' }))
    ].sort((a, b) => Number(b.pending || 0) - Number(a.pending || 0))
  } catch (e) {
    errors.value = { ...errors.value, owners: e.message || '归属人数据加载失败' }
  } finally {
    loading.value = { ...loading.value, owners: false }
  }
}

async function loadLots() {
  if (loading.value.lots) return
  loading.value = { ...loading.value, lots: true }
  errors.value = { ...errors.value, lots: '' }
  try {
    lots.value = await api.operationsLots()
  } catch (e) {
    errors.value = { ...errors.value, lots: e.message || '采购批次加载失败' }
  } finally {
    loading.value = { ...loading.value, lots: false }
  }
}

const targetMargin = ref(25)
const pricingQuery = ref('')
async function loadPricing(force = false) {
  if (loading.value.pricing && !force) return
  loading.value = { ...loading.value, pricing: true }
  errors.value = { ...errors.value, pricing: '' }
  try {
    pricing.value = await api.operationsPricing({ target_margin: targetMargin.value })
  } catch (e) {
    errors.value = { ...errors.value, pricing: e.message || '定价建议加载失败' }
  } finally {
    loading.value = { ...loading.value, pricing: false }
  }
}

async function loadMulti() {
  if (!props.globalAdmin || loading.value.multi) return
  loading.value = { ...loading.value, multi: true }
  errors.value = { ...errors.value, multi: '' }
  try {
    multi.value = await api.operationsMultiShop()
  } catch (e) {
    errors.value = { ...errors.value, multi: e.message || '多店汇总加载失败' }
  } finally {
    loading.value = { ...loading.value, multi: false }
  }
}

async function loadTodoCenter() {
  if (loading.value.todo) return
  loading.value = { ...loading.value, todo: true }
  errors.value = { ...errors.value, todo: '' }
  try {
    todoCenter.value = await api.operationsTodos()
  } catch (e) {
    errors.value = { ...errors.value, todo: e.message || '待办中心加载失败' }
  } finally {
    loading.value = { ...loading.value, todo: false }
  }
}

async function loadAudit() {
  if (loading.value.audit) return
  loading.value = { ...loading.value, audit: true }
  errors.value = { ...errors.value, audit: '' }
  try {
    const r = await api.operationsAudit({ limit: auditLimit.value, q: auditQuery.value.trim() })
    auditRows.value = r.items || []
  } catch (e) {
    errors.value = { ...errors.value, audit: e.message || '操作审计加载失败' }
  } finally {
    loading.value = { ...loading.value, audit: false }
  }
}

async function loadQualityTrend() {
  if (loading.value.quality) return
  loading.value = { ...loading.value, quality: true }
  errors.value = { ...errors.value, quality: '' }
  try {
    qualityTrend.value = await api.operationsQualityTrend(84)
  } catch (e) {
    errors.value = { ...errors.value, quality: e.message || '质量趋势加载失败' }
  } finally {
    loading.value = { ...loading.value, quality: false }
  }
}

const loaders = {
  health: loadHealth, transport: loadTransport, fees: loadFees,
  todo: loadTodoCenter, owners: loadOwnerBook, lots: loadLots, pricing: loadPricing,
  audit: loadAudit, quality: loadQualityTrend, multi: loadMulti
}
function ensureTab(key = tab.value) {
  loaders[key]?.()
}
function selectTab(key) {
  if (!tabs.value.some((item) => item.key === key)) return
  tab.value = key
  ensureTab(key)
}
onMounted(() => ensureTab())
watch(() => props.initialTab, (key) => {
  if (key) selectTab(key)
})
watch(() => props.refreshKey, () => {
  health.value = null
  transport.value = null
  fees.value = null
  ownerBook.value = null
  lots.value = null
  pricing.value = null
  multi.value = null
  todoCenter.value = null
  auditRows.value = []
  qualityTrend.value = null
  ensureTab()
})

const money = (v) => fmtMoney(Number(v) || 0)
const statusLabel = {
  matched: '已匹配', estimated: '估算匹配', pending: '待补单号',
  pending_bill: '待账单', pending_fix: '待补正', discrepancy: '有差异', reconciled: '已核销'
}
const statusClass = {
  matched: 'good', estimated: 'info', pending: 'warn',
  pending_bill: 'warn', pending_fix: 'info', discrepancy: 'bad', reconciled: 'good'
}

function issueAction(issue) {
  if (!issue) return
  if (issue.action === 'operations') {
    selectTab(issue.filter || 'fees')
    return
  }
  if (issue.action === 'settings') {
    emit('goto-settings', issue.filter || '')
    return
  }
  if (issue.action === 'consign') {
    emit('goto-consign', '寄卖')
    return
  }
  emit('goto-records', issue.filter ? { type: issue.filter } : {})
}
function openIssueItem(item) {
  if (!item?.id) return
  emit('goto-record', item.id)
}

const auditEntityLabels = {
  record: '交易记录', group: '联单', operations: '经营数据', sync: '同步',
  bill_import: '账单导入', settings: '设置', dataset: '数据', system: '系统'
}
function auditEntityLabel(row) {
  const label = auditEntityLabels[row?.entity_type] || row?.entity_type || '系统'
  return row?.entity_id ? `${label} #${row.entity_id}` : label
}
function auditCanOpen(row) {
  return !!row?.entity_id && (row.entity_type === 'record' || row.entity_type === 'group')
}
function openAuditEntity(row) {
  if (!auditCanOpen(row)) return
  if (row.entity_type === 'group') emit('goto-group', row.entity_id)
  else emit('goto-record', row.entity_id)
}
function auditChanges(row) {
  const changes = row?.detail?.changes
  if (!changes || typeof changes !== 'object') return []
  return Object.entries(changes).map(([key, value]) => ({
    key,
    before: value && typeof value === 'object' && 'before' in value ? value.before : '—',
    after: value && typeof value === 'object' && 'after' in value ? value.after : value
  }))
}
function auditValue(value) {
  if (value === null || value === undefined || value === '') return '空'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}
function searchAudit() {
  expandedAuditId.value = 0
  loadAudit()
}
const qualityPoints = computed(() => qualityTrend.value?.items || [])
const qualityStats = computed(() => {
  const points = qualityPoints.value
  if (!points.length) return null
  const current = points[points.length - 1]
  const first = points[0]
  const previous = points.length > 1 ? points[points.length - 2] : null
  const best = points.reduce((a, b) => Number(b.score) > Number(a.score) ? b : a)
  const worst = points.reduce((a, b) => Number(b.score) < Number(a.score) ? b : a)
  return {
    current,
    delta: Number(current.score) - Number(first.score),
    dayDelta: previous ? Number(current.score) - Number(previous.score) : 0,
    best,
    worst
  }
})
const qualityPath = computed(() => {
  const points = qualityPoints.value
  if (points.length < 2) return ''
  return points.map((point, index) => {
    const x = 22 + index * (676 / Math.max(1, points.length - 1))
    const y = 22 + (100 - Math.max(0, Math.min(100, Number(point.score) || 0))) * 1.36
    return `${index ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')
})
const qualityDots = computed(() => {
  const points = qualityPoints.value
  if (!points.length) return []
  return points.map((point, index) => ({
    ...point,
    x: 22 + index * (676 / Math.max(1, points.length - 1)),
    y: 22 + (100 - Math.max(0, Math.min(100, Number(point.score) || 0))) * 1.36
  }))
})

// ---------- 物流 CSV ----------
const transportRows = ref([])
const transportFileName = ref('')
const transportApply = ref(true)
const transportForce = ref(false)
const transportBusy = ref(false)

function parseCSVLine(line) {
  const cells = []
  let cur = ''
  let quoted = false
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (ch === '"') {
      if (quoted && line[i + 1] === '"') { cur += '"'; i++ }
      else quoted = !quoted
    } else if (ch === ',' && !quoted) {
      cells.push(cur.trim()); cur = ''
    } else {
      cur += ch
    }
  }
  cells.push(cur.trim())
  return cells
}
function detectColumn(headers, names) {
  const norm = (x) => String(x || '').toLowerCase().replace(/[\s_-]/g, '')
  const wanted = names.map(norm)
  return headers.findIndex((h) => wanted.includes(norm(h)))
}
function parseTransportFile(text) {
  const lines = String(text || '').replace(/^\uFEFF/, '').split(/\r?\n/).filter((x) => x.trim())
  if (!lines.length) return []
  const first = parseCSVLine(lines[0])
  const headerLike = first.some((h) => /订单|运单|运费|金额|时间|order|tracking|amount/i.test(h))
  const headers = headerLike ? first : ['订单号', '运单号', '快递公司', '运费', '时间', '备注']
  const data = headerLike ? lines.slice(1) : lines
  const iOrder = detectColumn(headers, ['订单号', '闲鱼订单号', 'order_no', 'order no', 'order'])
  const iTrack = detectColumn(headers, ['运单号', '快递单号', 'tracking_no', 'tracking', 'waybill'])
  const iCarrier = detectColumn(headers, ['快递公司', '物流公司', 'carrier', 'company'])
  const iAmount = detectColumn(headers, ['运费', '金额', '实付', 'amount', 'fee'])
  const iTime = detectColumn(headers, ['时间', '付款时间', '发货时间', 'occurred_at', 'date', 'time'])
  const iNote = detectColumn(headers, ['备注', '说明', 'note'])
  return data.map((line) => {
    const c = parseCSVLine(line)
    return {
      order_no: iOrder >= 0 ? c[iOrder] || '' : '',
      tracking_no: iTrack >= 0 ? c[iTrack] || '' : '',
      carrier: iCarrier >= 0 ? c[iCarrier] || '' : '',
      amount: Number(String(iAmount >= 0 ? c[iAmount] : '').replace(/[¥￥,\s]/g, '')) || 0,
      occurred_at: iTime >= 0 ? c[iTime] || '' : '',
      note: iNote >= 0 ? c[iNote] || '' : ''
    }
  }).filter((x) => x.amount > 0)
}
function onTransportFile(e) {
  const file = e.target.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    try {
      transportRows.value = parseTransportFile(reader.result)
      transportFileName.value = file.name
      emit('notify', `已读取 ${transportRows.value.length} 条运费，请确认后导入`, false, null, { tone: 'info' })
    } catch (err) {
      emit('notify', 'CSV 读取失败：' + (err.message || '格式不正确'), true)
    }
  }
  reader.readAsText(file, 'utf-8')
  e.target.value = ''
}
async function applyTransport() {
  if (!transportRows.value.length || transportBusy.value) return
  transportBusy.value = true
  try {
    const r = await api.operationsTransportImport(
      transportRows.value, transportApply.value, transportForce.value, transportFileName.value || 'CSV'
    )
    emit('notify', `物流账单已导入 ${r.imported} 条，自动补填 ${r.applied} 笔运费`)
    transportRows.value = []
    transportFileName.value = ''
    await Promise.all([loadTransport(), loadHealth()])
    emit('refresh')
  } catch (e) {
    emit('notify', e.message || '物流账单导入失败', true)
  } finally {
    transportBusy.value = false
  }
}
function downloadTransportTemplate() {
  const text = '\uFEFF订单号,运单号,快递公司,运费,时间,备注\nSY1234567890123456789,SF1234567890,顺丰,12.00,2026-11-23 10:00:00,示例行，请删除\n'
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  a.download = '鱼助手-物流费用导入模板.csv'
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}

// ---------- 服务费 ----------
const feePicked = ref({})
const feeBusy = ref(false)
function toggleFee(id) {
  feePicked.value = { ...feePicked.value, [id]: !feePicked.value[id] }
}
function toggleFeeAll() {
  const rows = fees.value?.items || []
  const active = rows.filter((x) => x.status !== 'reconciled')
  const all = active.length && active.every((x) => feePicked.value[x.item_id])
  const next = { ...feePicked.value }
  for (const r of active) next[r.item_id] = !all
  feePicked.value = next
}
async function reconcileFees(ids, note = '') {
  if (!ids.length || feeBusy.value) return
  feeBusy.value = true
  try {
    const r = await api.operationsFeeReconcile({ item_ids: ids, note, acknowledge: true })
    emit('notify', `已核销 ${r.updated} 笔服务费`)
    feePicked.value = {}
    await Promise.all([loadFees(), loadHealth()])
  } catch (e) {
    emit('notify', e.message || '核销失败', true)
  } finally {
    feeBusy.value = false
  }
}
function reconcilePicked() {
  const ids = Object.keys(feePicked.value).filter((id) => feePicked.value[id]).map(Number)
  reconcileFees(ids)
}
async function onAlipayRefresh() {
  await Promise.all([loadFees(), loadTransport(), loadHealth()])
  emit('refresh')
}

// ---------- 归属人对账单 ----------
const statementKind = ref('sell')
const statementFrom = ref('')
const statementTo = ref('')
const statement = ref(null)
const statementLoading = ref(false)
const selectedOwner = ref('')
const ownerSearch = ref('')
const filteredOwners = computed(() => {
  const q = ownerSearch.value.trim()
  return (ownerBook.value || []).filter((x) => !q || x.owner.includes(q) || x.kindLabel.includes(q))
})
async function openStatement(owner, kind = 'sell') {
  selectedOwner.value = owner
  statementKind.value = kind
  statementLoading.value = true
  statement.value = null
  try {
    statement.value = await api.operationsOwnerStatement(owner, kind, {
      from: statementFrom.value || '', to: statementTo.value || ''
    })
  } catch (e) {
    emit('notify', e.message || '对账单生成失败', true)
  } finally {
    statementLoading.value = false
  }
}
function printStatement() { window.print() }
async function copyStatement() {
  const s = statement.value
  if (!s) return
  const text = [
    `鱼助手 · ${s.owner} ${s.kind === 'buy' ? '代采' : '寄卖'}对账单`,
    `生成时间：${shortTime(s.generated_at)}`,
    `单据：${s.summary.orders} 笔`,
    `本期往来：${money(s.summary.period_total)}`,
    `累计往来：${money(s.summary.cumulative_total)}`,
    `累计已结：${money(s.summary.settled)}`,
    `当前待结：${money(s.summary.pending)}`
  ].join('\n')
  try {
    await navigator.clipboard.writeText(text)
    emit('notify', '对账摘要已复制')
  } catch (e) {
    emit('notify', '浏览器未允许复制，请手动选择文字', true)
  }
}

// ---------- 采购批次 ----------
const lotEdit = ref(null)
const lotBusy = ref(false)
function editLot(row) {
  lotEdit.value = {
    item_id: row.id, batch_no: row.batch_no || '', supplier: row.supplier || '', note: row.note || '',
    product: row.product, expected_updated_at: row.updated_at || ''
  }
}
async function saveLot() {
  if (!lotEdit.value || lotBusy.value) return
  lotBusy.value = true
  try {
    await api.saveLotMeta(lotEdit.value)
    emit('notify', '批次信息已保存')
    lotEdit.value = null
    await loadLots()
  } catch (e) {
    if (e?.status === 409 && e?.data?.code === 'conflict') {
      emit('notify', '批次已被其他人修改，已刷新为最新内容，请核对后再保存', true)
      lotEdit.value = null
      await loadLots()
      return
    }
    emit('notify', e.message || '批次信息保存失败', true)
  } finally {
    lotBusy.value = false
  }
}
function ageClass(days) {
  return days > 90 ? 'bad' : days > 60 ? 'warn' : days > 30 ? 'info' : 'good'
}

const shownPricing = computed(() => {
  const q = pricingQuery.value.trim().toLowerCase()
  return (pricing.value?.items || []).filter((x) => !q || x.product.toLowerCase().includes(q))
})
function usePrice(row) {
  emit('use-price', {
    product: row.product,
    sell_price: row.recommended_price,
    sell_shipping: row.shipping,
    sell_fee: Math.round(row.recommended_price * row.fee_rate) / 100,
    sell_wuyou_fee: 0
  })
}

const maxShopProfit = computed(() => Math.max(1, ...((multi.value?.items || []).map((x) => Math.max(0, Number(x.profit) || 0)))))
function openShop(row) {
  if (!row?.id) return
  switchShop(row.id)
  selectTab('health')
}
</script>

<template>
  <div class="page ops-page">
    <section class="ops-hero card">
      <div class="ops-hero-copy">
        <span class="ops-eyebrow">经营中心</span>
        <h2>把账做准，再决定下一步。</h2>
        <p>集中处理成本、运费、服务费、归属人与库存，不用在多个页面来回找。</p>
      </div>
      <div v-if="health" class="ops-score" :class="health.score >= 85 ? 'good' : health.score >= 65 ? 'warn' : 'bad'">
        <div class="ops-score-num">{{ health.score }}</div>
        <div class="ops-score-meta">
          <b>账本健康度</b>
          <span>{{ health.issueCount ? health.affected + ' 处需关注' : '当前没有明显异常' }}</span>
        </div>
      </div>
      <div v-else class="ops-score loading"><span class="spin"></span><span>正在体检…</span></div>
    </section>

    <nav class="ops-tabs" aria-label="经营中心功能">
      <button v-for="t in tabs" :key="t.key" :class="{ on: tab === t.key }" @click="selectTab(t.key)">
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path :d="t.icon" /></svg>
        <span>{{ t.label }}</span>
      </button>
    </nav>

    <div v-if="errors[tab]" class="ops-error card">
      <span>{{ errors[tab] }}</span>
      <button class="btn btn-sm" @click="ensureTab(tab)">重试</button>
    </div>

    <section v-if="tab === 'health'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>数据体检中心</h3><p>每条问题都带影响范围，可直接跳到对应单据处理。</p></div>
        <button class="btn btn-sm btn-quiet" :disabled="loading.health" @click="loadHealth">重新体检</button>
      </div>
      <div v-if="loading.health && !health" class="ops-skeleton">
        <div v-for="i in 4" :key="i" class="card skeleton ops-skel"></div>
      </div>
      <div v-else-if="health && !health.issues.length" class="card ops-empty">
        <span class="ops-empty-ico">✓</span>
        <b>账本状态很干净</b>
        <p>{{ health.checkedCount }} 条记录中没有发现待修数据。</p>
      </div>
      <div v-else-if="health" class="health-list">
        <article v-for="(issue, i) in health.issues" :key="issue.key" class="card health-card" :class="'lv-' + issue.severity" :style="{ '--delay': i * 45 + 'ms' }">
          <div class="health-top">
            <span class="health-dot"></span>
            <div class="health-copy">
              <div class="health-title"><b>{{ issue.title }}</b><span>{{ issue.count }} 项</span></div>
              <p>{{ issue.description }}</p>
            </div>
            <div class="health-amount">
              <span>影响金额</span>
              <b class="num">{{ money(issue.amount) }}</b>
            </div>
            <button class="btn btn-sm btn-primary" @click="issueAction(issue)">去处理</button>
          </div>
          <div v-if="issue.items?.length" class="health-items">
            <button v-for="it in issue.items.slice(0, 6)" :key="it.id" class="health-item" @click="openIssueItem(it)">
              <span class="cell-ellipsis">{{ it.product }}</span>
              <small>{{ shortTime(it.time) }}</small>
              <b class="num" :title="it.order_no">{{ money(it.amount) }}</b>
            </button>
            <span v-if="issue.items.length > 6" class="muted health-more">另 {{ issue.items.length - 6 }} 条</span>
          </div>
        </article>
      </div>
    </section>

    <section v-else-if="tab === 'todo'" class="ops-section" aria-live="polite">
      <div class="ops-section-head">
        <div><h3>经营待办中心</h3><p>把异常、同步失败与备份风险收在一处，做完一项就少一项。</p></div>
        <button class="btn btn-sm btn-quiet" :disabled="loading.todo" @click="loadTodoCenter">
          <span v-if="loading.todo" class="spin"></span>{{ loading.todo ? '正在刷新…' : '刷新待办' }}
        </button>
      </div>
      <div v-if="loading.todo && !todoCenter" class="ops-skeleton">
        <div v-for="i in 4" :key="'todo-skel-' + i" class="card skeleton ops-skel"></div>
      </div>
      <template v-else-if="todoCenter">
        <div class="todo-overview">
          <article class="card todo-score" :class="todoCenter.health.score >= 85 ? 'good' : todoCenter.health.score >= 65 ? 'warn' : 'bad'">
            <div class="todo-score-ring" :style="{ '--score': todoCenter.health.score + '%' }"><b>{{ todoCenter.health.score }}</b><small>分</small></div>
            <div><span>今日账本健康度</span><b>{{ todoCenter.health.score >= 85 ? '状态良好' : todoCenter.health.score >= 65 ? '需要留意' : '优先处理' }}</b><small>{{ todoCenter.health.checked_count }} 条记录已检查</small></div>
          </article>
          <article class="card todo-kpi"><span>待办合计</span><b>{{ todoCenter.total }}</b><small>{{ todoCenter.items.length }} 类问题</small></article>
          <article class="card todo-kpi"><span>影响金额</span><b class="num">{{ money(todoCenter.amount) }}</b><small>用于排优先级</small></article>
          <article class="card todo-kpi"><span>最近同步</span><b class="todo-time">{{ todoCenter.last_sync ? shortTime(todoCenter.last_sync) : '尚未同步' }}</b><small :class="{ neg: todoCenter.sync_error }">{{ todoCenter.sync_error ? '同步需要处理' : '连接状态正常' }}</small></article>
        </div>
        <div v-if="!todoCenter.items.length" class="card ops-empty todo-clean">
          <span class="ops-empty-ico">✓</span>
          <b>今天没有待处理事项</b>
          <p>账本、同步和备份都没有发现风险。新的异常出现时会在这里提醒。</p>
        </div>
        <div v-else class="todo-list">
          <article v-for="(issue, i) in todoCenter.items" :key="issue.key" class="card todo-item" :class="'lv-' + issue.severity" :style="{ '--delay': i * 40 + 'ms' }">
            <span class="todo-severity" aria-hidden="true"></span>
            <div class="todo-copy"><div><b>{{ issue.title }}</b><span class="todo-count">{{ issue.count }} 项</span></div><p>{{ issue.description }}</p><small v-if="issue.amount">{{ money(issue.amount) }} 受影响</small></div>
            <button class="btn btn-sm btn-primary todo-action" @click="issueAction(issue)">现在处理</button>
          </article>
        </div>
        <div v-if="todoCenter.sync_error" class="card todo-alert">
          <div><b>最近一次同步错误</b><p>{{ todoCenter.sync_error }}</p></div>
          <button class="btn btn-sm btn-quiet" @click="emit('goto-settings', 'goofish')">打开同步设置</button>
        </div>
        <p class="todo-generated">数据生成于 {{ shortTime(todoCenter.generated_at) }} · 每日首次体检会保存质量快照</p>
      </template>
    </section>

    <section v-else-if="tab === 'transport'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>物流费用中心</h3><p>导入快递或支付宝发货运费，按订单号、运单号或发货时间自动匹配。</p></div>
        <div class="ops-actions">
          <button class="btn btn-sm btn-quiet" @click="downloadTransportTemplate">下载 CSV 模板</button>
          <button class="btn btn-sm btn-quiet" @click="alipayOpen = true">导入支付宝账单</button>
        </div>
      </div>
      <div v-if="transport" class="ops-stats">
        <div class="card"><span>已匹配</span><b class="num">{{ fmtNum(transport.summary.matched) }}</b><small>{{ money(transport.summary.matched_amount) }}</small></div>
        <div class="card"><span>估算匹配</span><b class="num">{{ fmtNum(transport.summary.estimated) }}</b><small>按成交时间推断</small></div>
        <div class="card"><span>待补单号</span><b class="num">{{ fmtNum(transport.summary.pending) }}</b><small>{{ money(transport.summary.pending_amount) }}</small></div>
        <div class="card"><span>账单总额</span><b class="num">{{ money(transport.summary.total) }}</b><small>当前店铺</small></div>
      </div>
      <div class="card transport-import">
        <label class="transport-drop">
          <input type="file" accept=".csv,text/csv" hidden @change="onTransportFile" />
          <span class="transport-ico">⇧</span>
          <span><b>{{ transportFileName || '选择物流费用 CSV' }}</b><small>支持订单号、运单号、快递公司、运费、付款/发货时间</small></span>
        </label>
        <div v-if="transportRows.length" class="transport-preview">
          <div class="transport-preview-head">
            <b>已读取 {{ transportRows.length }} 条</b>
            <label><input v-model="transportApply" type="checkbox" /> 同时补填到卖单运费</label>
            <label><input v-model="transportForce" type="checkbox" /> 覆盖已完结/已有估算运费</label>
            <button class="btn btn-sm btn-primary" :disabled="transportBusy" @click="applyTransport">
              <span v-if="transportBusy" class="spin"></span>{{ transportBusy ? '导入中…' : '确认导入' }}
            </button>
          </div>
          <div class="transport-preview-list">
            <div v-for="(r, i) in transportRows.slice(0, 8)" :key="i">
              <span>{{ r.order_no || r.tracking_no || '仅时间匹配' }}</span>
              <small>{{ r.carrier || '快递' }} · {{ r.occurred_at || '无时间' }}</small>
              <b class="num">{{ money(r.amount) }}</b>
            </div>
          </div>
        </div>
      </div>
      <div v-if="transport" class="card ops-table-card">
        <div class="ops-table-head"><h3>最近物流账单</h3><span class="muted">共 {{ transport.items.length }} 条</span></div>
        <div v-if="transport.items.length" class="ops-table-scroll" role="region" aria-label="物流账单明细，可横向滚动" tabindex="0">
          <table class="ops-table">
            <thead><tr><th>状态</th><th>订单 / 运单</th><th>商品</th><th>物流</th><th>时间</th><th class="ta-r">运费</th></tr></thead>
            <tbody>
              <tr v-for="row in transport.items" :key="row.id">
                <td><span class="ops-status" :class="statusClass[row.status]">{{ statusLabel[row.status] || row.status }}</span></td>
                <td><b>{{ row.order_no || '—' }}</b><small>{{ row.tracking_no || '无运单号' }}</small></td>
                <td class="td-product">{{ row.product || '未匹配' }}</td>
                <td>{{ row.carrier || '—' }}<small>{{ row.source }}</small></td>
                <td>{{ shortTime(row.occurred_at) }}</td>
                <td class="ta-r num">{{ money(row.amount) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="ops-empty compact"><b>还没有物流账单</b><p>导入一次近期待发货订单即可自动补填。</p></div>
      </div>
      <div v-if="transport?.pending_sales?.length" class="card pending-shipping">
        <div class="ops-table-head"><h3>近期未填运费</h3><span class="muted">{{ transport.pending_sales.length }} 笔</span></div>
        <div class="pending-ship-list">
          <button v-for="r in transport.pending_sales.slice(0, 12)" :key="r.id" @click="emit('goto-record', r.id)">
            <span class="cell-ellipsis">{{ r.product }}</span><small>{{ r.order_no || '无订单号' }}</small><b class="num">{{ money(r.amount) }}</b>
          </button>
        </div>
      </div>
    </section>

    <section v-else-if="tab === 'fees'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>服务费核销中心</h3><p>软件服务费 = 软件服务费 + 基础软件服务费；无忧卖单独列入总费用。</p></div>
        <button class="btn btn-sm btn-quiet" @click="alipayOpen = true">导入支付宝账单</button>
      </div>
      <div v-if="fees" class="ops-stats five">
        <div class="card"><span>待账单</span><b>{{ fees.summary.pending_bill }}</b><small>尚无实际扣费</small></div>
        <div class="card"><span>待补正</span><b>{{ fees.summary.pending_fix }}</b><small>基本一致，待确认</small></div>
        <div class="card"><span>有差异</span><b class="neg">{{ fees.summary.discrepancy }}</b><small>预计 {{ money(fees.summary.difference) }}</small></div>
        <div class="card"><span>已核销</span><b class="pos">{{ fees.summary.reconciled }}</b><small>已人工确认</small></div>
        <div class="card"><span>实际 / 预计</span><b class="dual-num">{{ money(fees.summary.actual) }}</b><small>预计 {{ money(fees.summary.expected) }}</small></div>
      </div>
      <div v-if="fees" class="card ops-table-card">
        <div class="ops-table-head">
          <h3>费用核对明细</h3>
          <button class="btn btn-sm" :disabled="feeBusy || !Object.values(feePicked).some(Boolean)" @click="reconcilePicked">
            核销已选 {{ Object.values(feePicked).filter(Boolean).length || '' }}
          </button>
        </div>
        <div v-if="fees.items.length" class="ops-table-scroll" role="region" aria-label="服务费核对明细，可横向滚动" tabindex="0">
          <table class="ops-table fee-table">
            <thead><tr><th><input type="checkbox" aria-label="全选待核销" @change="toggleFeeAll" /></th><th>状态</th><th>商品 / 订单</th><th class="ta-r">成交价</th><th class="ta-r">软件费</th><th class="ta-r">无忧卖</th><th class="ta-r">实际</th><th class="ta-r">预计</th><th class="ta-r">差异</th><th></th></tr></thead>
            <tbody>
              <tr v-for="row in fees.items" :key="row.item_id" :class="{ reconciled: row.status === 'reconciled' }">
                <td><input type="checkbox" :checked="!!feePicked[row.item_id]" :disabled="row.status === 'reconciled'" :aria-label="`选择 ${row.product}${row.order_no ? '，订单 ' + row.order_no : ''}`" @change="toggleFee(row.item_id)" /></td>
                <td><span class="ops-status" :class="statusClass[row.status]">{{ statusLabel[row.status] }}</span></td>
                <td><button class="table-link" @click="emit('goto-record', row.item_id)">{{ row.product }}</button><small>{{ row.order_no || shortTime(row.time) }}</small></td>
                <td class="ta-r num">{{ money(row.price) }}</td>
                <td class="ta-r num">{{ money(row.software_fee) }}</td>
                <td class="ta-r num">{{ money(row.wuyou_fee) }}</td>
                <td class="ta-r num">{{ money(row.actual_fee) }}</td>
                <td class="ta-r num">{{ money(row.expected_fee) }}<small>{{ row.expected_rate.toFixed(1) }}%</small></td>
                <td class="ta-r num" :class="row.difference > 0 ? 'neg' : row.difference < 0 ? 'pos' : ''">{{ money(row.difference) }}</td>
                <td><button v-if="row.status !== 'reconciled'" class="btn btn-sm btn-quiet" :disabled="feeBusy" @click="reconcileFees([row.item_id])">确认</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="ops-empty compact"><b>还没有成交费用数据</b><p>同步订单后会自动生成核对清单。</p></div>
      </div>
    </section>

    <section v-else-if="tab === 'owners'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>归属人对账单</h3><p>寄卖与代采分开核算，包含商品、费用、待结与结算历史。</p></div>
        <div class="statement-range"><input v-model="statementFrom" class="input" type="date" aria-label="对账开始日期" /><span>至</span><input v-model="statementTo" class="input" type="date" aria-label="对账结束日期" /></div>
      </div>
      <div class="owner-layout">
        <aside class="card owner-picker">
          <div class="ops-table-head"><h3>选择归属人</h3><span class="muted">{{ filteredOwners.length }} 人</span></div>
          <div class="owner-search"><input v-model="ownerSearch" class="input" placeholder="搜索寄卖人或代采委托人" /></div>
          <div v-if="ownerBook" class="owner-list">
            <button v-for="row in filteredOwners" :key="row.kind + row.owner" :class="{ on: statement?.owner === row.owner && statement?.kind === row.kind }" @click="openStatement(row.owner, row.kind)">
              <span class="owner-avatar">{{ row.owner.slice(0, 1) }}</span>
              <span class="owner-name"><b>{{ row.owner }}</b><small>{{ row.kindLabel }} · {{ row.orders }} 单</small></span>
              <span class="owner-money"><b class="num">{{ money(row.pending) }}</b><small>待结</small></span>
            </button>
            <div v-if="!filteredOwners.length" class="ops-empty compact"><b>没有匹配的归属人</b></div>
          </div>
          <div v-else class="ops-skeleton compact"><div class="skeleton ops-skel"></div><div class="skeleton ops-skel"></div></div>
        </aside>
        <article class="card statement-sheet">
          <div v-if="statementLoading" class="ops-empty"><span class="spin"></span><p>正在生成对账单…</p></div>
          <div v-else-if="!statement" class="ops-empty"><span class="ops-empty-ico">账</span><b>选择一位归属人</b><p>生成后可直接打印或保存为 PDF。</p></div>
          <div v-else class="statement-print">
            <header class="statement-head">
              <div class="statement-title"><span class="ops-eyebrow">鱼助手 · {{ statement.kind === 'buy' ? '代采' : '寄卖' }}</span><h2>{{ statement.owner }} 对账单</h2><p>期间：{{ statement.from || '全部' }} 至 {{ statement.to || '今天' }} · 生成 {{ shortTime(statement.generated_at) }}</p></div>
              <div class="statement-actions ops-nonprint"><button class="btn btn-sm btn-quiet" @click="copyStatement">复制摘要</button><button class="btn btn-sm btn-primary" @click="printStatement">打印 / 存 PDF</button></div>
            </header>
            <div class="statement-totals">
              <div class="statement-total is-period"><span>本期往来</span><b class="num">{{ money(statement.summary.period_total) }}</b><small>当前筛选期间</small></div>
              <div class="statement-total"><span>累计往来</span><b class="num">{{ money(statement.summary.cumulative_total) }}</b><small>全部业务记录</small></div>
              <div class="statement-total"><span>累计已结</span><b class="num">{{ money(statement.summary.settled) }}</b><small>已完成结算</small></div>
              <div class="statement-total is-pending"><span>当前待结</span><b class="num pos">{{ money(statement.summary.pending) }}</b><small>仍需核对</small></div>
            </div>
            <div class="statement-table" role="region" aria-label="对账单明细，可横向滚动" tabindex="0">
              <table>
                <thead><tr><th>时间</th><th>商品 / 订单</th><th class="ta-r">{{ statement.kind === 'buy' ? '垫付金额' : '成交价' }}</th><th class="ta-r">费用</th><th class="ta-r">{{ statement.kind === 'buy' ? '运费' : '到手' }}</th></tr></thead>
                <tbody>
                  <tr v-for="row in statement.items" :key="row.id">
                    <td>{{ shortTime(row.time) }}</td>
                    <td><b>{{ row.product }}</b><small>{{ row.order_no || '—' }}</small></td>
                    <td class="ta-r num">{{ money(statement.kind === 'buy' ? row.buy_amount : row.price) }}</td>
                    <td class="ta-r num">{{ money(row.fee) }}</td>
                    <td class="ta-r num">{{ money(statement.kind === 'buy' ? row.shipping : row.net) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="statement-history">
              <h3>结算历史</h3>
              <div v-if="statement.settlements.length">
                <div v-for="s in statement.settlements" :key="s.id"><span>{{ shortTime(s.settled_at) }} · {{ s.note || '结算' }}</span><b class="num">{{ money(s.amount) }}</b></div>
              </div>
              <p v-else class="muted">暂无结算记录</p>
            </div>
            <footer class="statement-foot">本对账单由鱼助手根据当前店铺单据与结算流水生成，最终金额请双方核对确认。</footer>
          </div>
        </article>
      </div>
    </section>

    <section v-else-if="tab === 'lots'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>采购批次与库龄</h3><p>按现有买单、卖单和联单分摊派生剩余成本，不改变成本规则。</p></div>
        <button class="btn btn-sm btn-quiet" @click="loadLots">刷新</button>
      </div>
      <div v-if="lots" class="ops-stats">
        <div class="card"><span>采购批次</span><b>{{ lots.summary.batches }}</b><small>{{ lots.summary.active }} 批仍有库存</small></div>
        <div class="card"><span>累计落地成本</span><b class="num">{{ money(lots.summary.total_landed) }}</b><small>买价 + 买入运费</small></div>
        <div class="card"><span>当前占用</span><b class="num">{{ money(lots.summary.remaining) }}</b><small>未售出部分</small></div>
        <div class="card"><span>90 天以上</span><b class="num neg">{{ money(lots.summary.old_remaining) }}</b><small>建议优先清库</small></div>
      </div>
      <div v-if="lots" class="lot-grid">
        <article v-for="row in lots.items" :key="row.id" class="card lot-card" :class="{ sold: row.sold_out }">
          <div class="lot-head"><span class="ops-status" :class="ageClass(row.age_days)">{{ row.age_band }}</span><span class="muted">{{ row.age_days }} 天</span></div>
          <h3>{{ row.product }}</h3>
          <p>{{ row.batch_no || '未编批次号' }} · {{ row.supplier || '未填供应商' }}</p>
          <div class="lot-money">
            <div><span>单件落地</span><b class="num">{{ money(row.unit_landed) }}</b></div>
            <div><span>剩余占用</span><b class="num">{{ money(row.remaining_cost) }}</b></div>
          </div>
          <div class="lot-bar"><i :style="{ width: Math.min(100, row.remaining_cost / Math.max(1, row.landed_cost) * 100) + '%' }"></i></div>
          <div class="lot-foot"><span>剩余 {{ row.remaining_qty }} / {{ row.quantity }} 件</span><button class="btn btn-sm btn-quiet" @click="editLot(row)">编辑批次</button></div>
          <div v-if="lotEdit?.item_id === row.id" class="lot-edit">
            <input v-model="lotEdit.batch_no" class="input" placeholder="批次号" />
            <input v-model="lotEdit.supplier" class="input" placeholder="供应商" />
            <input v-model="lotEdit.note" class="input" placeholder="备注" />
            <div><button class="btn btn-sm" @click="lotEdit = null">取消</button><button class="btn btn-sm btn-primary" :disabled="lotBusy" @click="saveLot">保存</button></div>
          </div>
        </article>
      </div>
      <div v-if="lots && !lots.items.length" class="card ops-empty"><b>还没有采购批次</b><p>录入或同步买进单后会在这里按批次展示。</p></div>
    </section>

    <section v-else-if="tab === 'pricing'" class="ops-section">
      <div class="ops-section-head">
        <div><h3>智能定价与补货建议</h3><p>结合目标利润率、类目服务费、实际运费、历史成交价和库龄。</p></div>
        <div class="pricing-controls"><label>目标利润率 <b>{{ targetMargin }}%</b></label><input v-model.number="targetMargin" aria-label="目标利润率" type="range" min="5" max="60" step="1" @change="loadPricing(true)" /></div>
      </div>
      <div v-if="pricing" class="pricing-note card"><span>计算口径</span><p>{{ pricing.assumption }}</p><input v-model="pricingQuery" class="input" placeholder="搜索商品" /></div>
      <div v-if="pricing" class="pricing-grid">
        <article v-for="row in shownPricing" :key="row.norm" class="card pricing-card" :class="{ restock: row.restock }">
          <div class="pricing-head"><span v-if="row.restock" class="ops-status good">建议补货</span><span v-else class="ops-status info">{{ row.sales }} 单历史</span><span class="muted">库龄 {{ row.stock_age_days }} 天</span></div>
          <h3>{{ row.product }}</h3>
          <div class="price-main"><span>建议挂牌</span><b class="num">{{ money(row.recommended_price) }}</b><small>最低 {{ money(row.floor_price) }}</small></div>
          <div class="price-grid">
            <div><span>单件成本</span><b class="num">{{ money(row.unit_cost) }}</b></div>
            <div><span>实际运费</span><b class="num">{{ money(row.shipping) }}</b></div>
            <div><span>预计费用率</span><b>{{ row.fee_rate.toFixed(1) }}%</b></div>
            <div><span>预期利润</span><b class="num pos">{{ money(row.expected_profit) }}</b></div>
          </div>
          <p class="pricing-reason">{{ row.reason }}</p>
          <div class="pricing-foot"><span>历史 {{ money(row.low_price) }} – {{ money(row.high_price) }}</span><button class="btn btn-sm btn-primary" @click="usePrice(row)">按此价录入</button></div>
        </article>
      </div>
      <div v-if="pricing && !shownPricing.length" class="card ops-empty"><b>没有可用于定价的商品</b><p>需要有采购成本或历史成交价。</p></div>
    </section>

    <section v-else-if="tab === 'audit'" class="ops-section" aria-live="polite">
      <div class="ops-section-head">
        <div><h3>长期操作审计</h3><p>保留新增、修改、删除和关键金额变化，便于多人协作时追溯。</p></div>
        <button class="btn btn-sm btn-quiet" :disabled="loading.audit" @click="loadAudit"><span v-if="loading.audit" class="spin"></span>{{ loading.audit ? '读取中…' : '刷新记录' }}</button>
      </div>
      <form class="card audit-toolbar" @submit.prevent="searchAudit">
        <label class="audit-search">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.6-3.6"/></svg>
          <input v-model="auditQuery" class="input" placeholder="搜索操作者、操作内容或实体类型" aria-label="搜索操作审计" />
        </label>
        <select v-model.number="auditLimit" class="select" aria-label="显示条数" @change="loadAudit">
          <option :value="50">最近 50 条</option>
          <option :value="100">最近 100 条</option>
          <option :value="300">最近 300 条</option>
        </select>
        <button class="btn btn-primary" :disabled="loading.audit" type="submit">搜索</button>
      </form>
      <div v-if="loading.audit && !auditRows.length" class="ops-skeleton">
        <div v-for="i in 4" :key="'audit-skel-' + i" class="card skeleton ops-skel"></div>
      </div>
      <div v-else-if="!auditRows.length" class="card ops-empty">
        <span class="ops-empty-ico">录</span><b>{{ auditQuery ? '没有匹配的审计记录' : '暂时还没有写操作' }}</b>
        <p>{{ auditQuery ? '换个关键词，或清空搜索后再看。' : '修改订单、联单、批次或设置后，会自动记录在这里。' }}</p>
      </div>
      <div v-else class="audit-list">
        <article v-for="row in auditRows" :key="row.id" class="card audit-row" :class="{ open: expandedAuditId === row.id }">
          <button class="audit-summary" type="button" :aria-expanded="expandedAuditId === row.id" @click="expandedAuditId = expandedAuditId === row.id ? 0 : row.id">
            <span class="audit-time"><b>{{ shortTime(row.created_at) }}</b><small>{{ row.actor || '未知操作者' }}</small></span>
            <span class="audit-main-copy"><b>{{ row.summary || row.action }}</b><small>{{ auditEntityLabel(row) }} · {{ row.action }}</small></span>
            <span class="audit-open-mark">{{ expandedAuditId === row.id ? '收起' : '查看' }}<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2"><path d="M6 9l6 6 6-6"/></svg></span>
          </button>
          <div v-if="expandedAuditId === row.id" class="audit-detail">
            <div v-if="auditChanges(row).length" class="audit-changes">
              <div v-for="change in auditChanges(row)" :key="change.key"><span>{{ change.key }}</span><del>{{ auditValue(change.before) }}</del><b>{{ auditValue(change.after) }}</b></div>
            </div>
            <p v-else>本次操作没有可展开的字段差异。</p>
            <button v-if="auditCanOpen(row)" class="btn btn-sm btn-quiet" @click="openAuditEntity(row)">打开对应{{ row.entity_type === 'group' ? '联单' : '记录' }}</button>
          </div>
        </article>
      </div>
    </section>

    <section v-else-if="tab === 'quality'" class="ops-section quality-section" aria-live="polite">
      <div class="ops-section-head">
        <div><h3>数据质量趋势</h3><p>观察最近 84 天健康分与待补数量，确认问题是在收敛还是反复出现。</p></div>
        <button class="btn btn-sm btn-quiet" :disabled="loading.quality" @click="loadQualityTrend"><span v-if="loading.quality" class="spin"></span>{{ loading.quality ? '读取中…' : '刷新趋势' }}</button>
      </div>
      <div v-if="loading.quality && !qualityTrend" class="card skeleton quality-chart-skeleton"></div>
      <template v-else-if="qualityStats">
        <div class="ops-stats quality-kpis">
          <div class="card"><span>今日健康分</span><b :class="qualityStats.current.score >= 85 ? 'pos' : qualityStats.current.score >= 65 ? 'warn-text' : 'neg'">{{ qualityStats.current.score }}</b><small>较昨日 {{ qualityStats.dayDelta >= 0 ? '+' : '' }}{{ qualityStats.dayDelta }}</small></div>
          <div class="card"><span>区间变化</span><b :class="qualityStats.delta >= 0 ? 'pos' : 'neg'">{{ qualityStats.delta >= 0 ? '+' : '' }}{{ qualityStats.delta }}</b><small>最近一次检查有 {{ qualityStats.current.affected }} 处待关注</small></div>
          <div class="card"><span>最好水平</span><b>{{ qualityStats.best.score }}</b><small>{{ qualityStats.best.date }}</small></div>
          <div class="card"><span>最低水平</span><b>{{ qualityStats.worst.score }}</b><small>{{ qualityStats.worst.date }}</small></div>
        </div>
        <div class="card quality-chart">
          <div class="quality-chart-head"><div><b>健康度走势</b><span>纵轴 0–100 分，连续查看 84 天</span></div><span class="quality-legend"><i></i>健康分</span></div>
          <svg viewBox="0 0 720 180" role="img" aria-label="最近数据质量健康分趋势折线图">
            <defs>
              <linearGradient id="quality-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="var(--brand)" stop-opacity=".24"/><stop offset="1" stop-color="var(--brand)" stop-opacity="0"/></linearGradient>
            </defs>
            <g class="quality-grid"><path d="M22 22H698M22 56H698M22 90H698M22 124H698M22 158H698"/><text x="2" y="26">100</text><text x="6" y="94">75</text><text x="6" y="162">50</text></g>
            <path v-if="qualityPath && qualityDots.length > 1" :d="qualityPath + ` L${qualityDots[qualityDots.length - 1].x},158 L${qualityDots[0].x},158 Z`" fill="url(#quality-fill)" stroke="none"/>
            <path v-if="qualityPath" :d="qualityPath" class="quality-line"/>
            <g v-for="point in qualityDots" :key="point.date" class="quality-dot"><circle :cx="point.x" :cy="point.y" r="4"><title>{{ point.date }}：{{ point.score }} 分，{{ point.issue_count }} 项问题</title></circle></g>
          </svg>
          <div class="quality-chart-foot"><span>{{ qualityPoints[0]?.date }}</span><b>{{ qualityPoints.length }} 个检查日</b><span>{{ qualityPoints[qualityPoints.length - 1]?.date }}</span></div>
        </div>
        <div class="card quality-history">
          <div class="ops-table-head"><h3>每日检查记录</h3><span class="muted">最新在前</span></div>
          <div class="quality-history-list">
            <div v-for="point in [...qualityPoints].reverse().slice(0, 12)" :key="'history-' + point.date">
              <span>{{ point.date }}</span><b :class="point.score >= 85 ? 'pos' : point.score >= 65 ? 'warn-text' : 'neg'">{{ point.score }} 分</b><small>{{ point.issue_count }} 类 · {{ point.affected }} 处</small><em class="num">{{ money(point.impact) }}</em>
            </div>
          </div>
        </div>
      </template>
      <div v-else class="card ops-empty">
        <span class="ops-empty-ico">质</span><b>还没有质量快照</b><p>先刷新一次待办中心，系统会把今天的体检结果记录为趋势起点。</p>
        <button class="btn btn-sm btn-primary" @click="selectTab('todo')">去待办中心生成快照</button>
      </div>
    </section>

    <section v-else-if="tab === 'multi'" class="ops-section">
      <div class="ops-section-head"><div><h3>多店经营驾驶舱</h3><p>仅全局管理员可见；店铺密码会话不会读取其他店铺。</p></div><button class="btn btn-sm btn-quiet" @click="loadMulti">刷新</button></div>
      <div v-if="multi" class="shop-cockpit">
        <article v-for="row in multi.items" :key="row.id" class="card cockpit-card" @click="openShop(row)">
          <div class="cockpit-head"><i :style="{ background: row.color || 'var(--brand)' }"></i><b>{{ row.name }}</b><span class="num" :class="row.profit >= 0 ? 'pos' : 'neg'">{{ money(row.profit) }}</span></div>
          <div class="cockpit-bar"><i :style="{ width: Math.max(2, Math.max(0, row.profit) / maxShopProfit * 100) + '%' }"></i></div>
          <div class="cockpit-grid"><div><span>销售额</span><b class="num">{{ money(row.sales) }}</b></div><div><span>利润率</span><b>{{ row.margin.toFixed(1) }}%</b></div><div><span>在库</span><b class="num">{{ money(row.holding) }}</b></div><div><span>待补成本</span><b>{{ row.pending_cost }} 单</b></div></div>
          <small>待补服务费 {{ row.fee_pending }} 单 · 点击切换到此店铺</small>
        </article>
      </div>
      <div v-if="multi && !multi.items.length" class="card ops-empty"><b>还没有店铺</b></div>
    </section>

    <div v-if="isBusy && !((tab === 'health' && health) || (tab === 'todo' && todoCenter) || (tab === 'transport' && transport) || (tab === 'fees' && fees) || (tab === 'owners' && ownerBook) || (tab === 'lots' && lots) || (tab === 'pricing' && pricing) || (tab === 'audit' && auditRows.length) || (tab === 'quality' && qualityTrend) || (tab === 'multi' && multi))" class="card ops-loading"><span class="spin"></span><span>正在加载{{ tabLabel }}…</span></div>

    <AlipayImport v-if="alipayOpen" @close="alipayOpen = false" @notify="(...args) => emit('notify', ...args)" @refresh="onAlipayRefresh" />
  </div>
</template>

<style scoped>
.ops-page { gap: 16px; }
.ops-hero {
  position: relative; overflow: hidden; min-height: 132px; padding: 24px 28px;
  display: flex; align-items: center; justify-content: space-between; gap: 24px;
  background:
    radial-gradient(circle at 88% -10%, rgba(245,181,0,.24), transparent 36%),
    radial-gradient(circle at 10% 120%, rgba(36,87,214,.10), transparent 42%),
    linear-gradient(135deg, var(--surface), var(--brand-soft));
}
.ops-hero::after { content: ""; position: absolute; inset: auto -30px -80px auto; width: 220px; height: 220px; border-radius: 50%; border: 28px solid rgba(245,181,0,.08); }
.ops-hero-copy { position: relative; z-index: 1; }
.ops-eyebrow { color: var(--brand-ink); font-size: 11px; font-weight: 800; letter-spacing: .16em; text-transform: uppercase; }
.ops-hero h2 { margin: 8px 0 5px; font-size: 24px; letter-spacing: -.5px; }
.ops-hero p { margin: 0; color: var(--text-2); font-size: 13px; max-width: 660px; }
.ops-score { position: relative; z-index: 1; flex: 0 0 auto; min-width: 176px; display: flex; align-items: center; gap: 13px; padding: 14px 18px; border: 1px solid var(--border); border-radius: 16px; background: rgba(255,255,255,.72); backdrop-filter: blur(8px); }
html[data-theme="dark"] .ops-score { background: rgba(28,34,43,.82); }
.ops-score-num { font-size: 37px; line-height: 1; font-weight: 850; letter-spacing: -2px; }
.ops-score.good .ops-score-num { color: var(--down); }
.ops-score.warn .ops-score-num { color: #aa6b00; }
.ops-score.bad .ops-score-num { color: var(--up); }
html[data-theme="dark"] .ops-score.warn .ops-score-num { color: #fbbf24; }
.ops-score-meta { display: grid; gap: 2px; }
.ops-score-meta b { font-size: 13px; }
.ops-score-meta span { color: var(--text-3); font-size: 11.5px; }
.ops-score.loading { color: var(--text-2); font-size: 13px; }
.ops-tabs { display: flex; gap: 7px; overflow-x: auto; padding-bottom: 2px; scrollbar-width: none; }
.ops-tabs::-webkit-scrollbar { display: none; }
.ops-tabs button { min-height: 42px; flex: 0 0 auto; padding: 0 14px; border: 1px solid var(--border); border-radius: 12px; background: var(--surface); color: var(--text-2); display: inline-flex; align-items: center; gap: 7px; font-weight: 650; cursor: pointer; transition: .18s ease; }
.ops-tabs button:hover { border-color: var(--brand-deep); color: var(--text); transform: translateY(-1px); }
.ops-tabs button.on { background: var(--brand-soft); border-color: rgba(245,181,0,.42); color: var(--brand-ink); box-shadow: 0 4px 14px rgba(245,181,0,.13); }
.ops-section { display: flex; flex-direction: column; gap: 14px; animation: opsIn .28s ease both; }
@keyframes opsIn { from { transform: translateY(5px); } }
.ops-section-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; }
.ops-section-head h3 { margin: 0; font-size: 18px; }
.ops-section-head p { margin: 4px 0 0; color: var(--text-3); font-size: 12.5px; }
.ops-actions { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.ops-error { padding: 14px 18px; display: flex; align-items: center; justify-content: space-between; color: var(--up); border-color: rgba(197,60,38,.25); }
.health-list { display: grid; gap: 10px; }
.health-card { padding: 16px 18px; animation: opsIn .35s ease both; animation-delay: var(--delay); border-left: 4px solid var(--border-strong); }
.health-card.lv-danger { border-left-color: var(--up); }
.health-card.lv-warn { border-left-color: #d68a00; }
.health-card.lv-info { border-left-color: var(--blue); }
.health-top { display: flex; align-items: center; gap: 13px; }
.health-dot { width: 11px; height: 11px; flex: 0 0 auto; border-radius: 50%; background: var(--blue); box-shadow: 0 0 0 5px var(--blue-soft); }
.lv-danger .health-dot { background: var(--up); box-shadow: 0 0 0 5px var(--up-soft); }
.lv-warn .health-dot { background: #d68a00; box-shadow: 0 0 0 5px rgba(214,138,0,.12); }
.health-copy { flex: 1; min-width: 0; }
.health-title { display: flex; align-items: center; gap: 8px; }
.health-title b { font-size: 14px; }
.health-title span { padding: 2px 7px; border-radius: 999px; background: var(--surface-2); color: var(--text-3); font-size: 10.5px; }
.health-copy p { margin: 3px 0 0; color: var(--text-3); font-size: 12px; }
.health-amount { text-align: right; min-width: 108px; }
.health-amount span { display: block; color: var(--text-3); font-size: 10.5px; }
.health-amount b { font-size: 16px; }
.health-items { margin: 12px 0 0 24px; padding-top: 10px; border-top: 1px dashed var(--border); display: grid; grid-template-columns: repeat(auto-fit,minmax(190px,1fr)); gap: 5px 12px; }
.health-item { min-width: 0; border: 0; background: transparent; color: var(--text); display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 1px 8px; padding: 5px 7px; border-radius: 8px; text-align: left; cursor: pointer; }
.health-item:hover { background: var(--surface-2); }
.health-item small { grid-column: 1; color: var(--text-3); font-size: 10.5px; }
.health-item b { grid-column: 2; grid-row: 1/3; align-self: center; font-size: 11.5px; }
.health-more { align-self: center; font-size: 11px; }
.ops-stats { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 10px; }
.ops-stats.five { grid-template-columns: repeat(5,minmax(0,1fr)); }
.ops-stats .card { padding: 14px 16px; display: grid; gap: 2px; }
.ops-stats span { color: var(--text-3); font-size: 11.5px; }
.ops-stats b { font-size: 24px; line-height: 1.2; }
.ops-stats small { color: var(--text-3); font-size: 10.5px; }
.ops-stats .dual-num { font-size: 19px; }
.transport-import { padding: 16px; display: grid; gap: 14px; }
.transport-drop { min-height: 106px; border: 1.5px dashed var(--border-strong); border-radius: 14px; display: flex; align-items: center; justify-content: center; gap: 13px; cursor: pointer; background: var(--surface-2); transition: .18s; }
.transport-drop:hover { border-color: var(--brand-deep); background: var(--brand-soft); }
.transport-ico { width: 42px; height: 42px; border-radius: 12px; display: grid; place-items: center; background: var(--surface); color: var(--brand-deep); font-size: 23px; box-shadow: var(--shadow); }
.transport-drop span:last-child { display: grid; gap: 3px; }
.transport-drop small { color: var(--text-3); }
.transport-preview { border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }
.transport-preview-head { padding: 10px 12px; display: flex; align-items: center; gap: 14px; flex-wrap: wrap; background: var(--surface-2); }
.transport-preview-head label { color: var(--text-2); font-size: 12px; display: inline-flex; align-items: center; gap: 5px; }
.transport-preview-head .btn { margin-left: auto; }
.transport-preview-list { max-height: 230px; overflow: auto; }
.transport-preview-list > div { padding: 8px 12px; display: grid; grid-template-columns: 1fr auto; gap: 1px 10px; border-top: 1px solid var(--border); }
.transport-preview-list small { grid-row: 2; color: var(--text-3); }
.transport-preview-list b { grid-column: 2; grid-row: 1/3; align-self: center; }
.ops-table-card { overflow: hidden; }
.ops-table-head { min-height: 52px; padding: 10px 16px; display: flex; align-items: center; gap: 12px; }
.ops-table-head h3 { margin: 0; font-size: 14px; flex: 1; }
.ops-table-head .btn { margin-left: auto; }
.ops-table-scroll { overflow-x: auto; }
.ops-table-scroll:focus-visible,
.statement-table:focus-visible { outline: 2px solid var(--brand-deep); outline-offset: 2px; }
.ops-table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.ops-table th { padding: 9px 12px; color: var(--text-3); font-size: 10.5px; font-weight: 700; text-align: left; white-space: nowrap; background: var(--surface-2); border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); }
.ops-table td { padding: 10px 12px; border-bottom: 1px solid var(--border); vertical-align: middle; }
.ops-table tr:last-child td { border-bottom: 0; }
.ops-table tbody tr:hover { background: var(--surface-2); }
.ops-table td small { display: block; color: var(--text-3); font-size: 10.5px; margin-top: 1px; }
.ops-table .ta-r { text-align: right; }
.td-product { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ops-status { display: inline-flex; align-items: center; min-height: 21px; padding: 2px 7px; border-radius: 999px; font-size: 10.5px; font-weight: 700; white-space: nowrap; }
.ops-status.good { background: var(--down-soft); color: var(--down); }
.ops-status.warn { background: rgba(214,138,0,.12); color: #9a6200; }
.ops-status.bad { background: var(--up-soft); color: var(--up); }
.ops-status.info { background: var(--blue-soft); color: var(--blue); }
html[data-theme="dark"] .ops-status.warn { background: rgba(251,191,36,.14); color: #fbbf24; }
html[data-theme="dark"] .health-card.lv-warn { border-left-color: #fbbf24; }
html[data-theme="dark"] .lv-warn .health-dot { background: #fbbf24; box-shadow: 0 0 0 5px rgba(251,191,36,.14); }
.fee-table tr.reconciled { opacity: .72; }
.table-link { border: 0; background: transparent; padding: 0; color: var(--text); font-weight: 650; text-align: left; cursor: pointer; }
.table-link:hover { color: var(--blue); text-decoration: underline; }
.ops-empty { padding: 42px 20px; display: grid; justify-items: center; align-content: center; text-align: center; gap: 5px; color: var(--text-3); }
.ops-empty.compact { padding: 24px 16px; }
.ops-empty b { color: var(--text); }
.ops-empty p { margin: 0; font-size: 12px; }
.ops-empty-ico { width: 40px; height: 40px; display: grid; place-items: center; border-radius: 13px; background: var(--brand-soft); color: var(--brand-ink); font-size: 20px; font-weight: 800; margin-bottom: 4px; }
.pending-ship-list { display: grid; grid-template-columns: repeat(auto-fit,minmax(220px,1fr)); gap: 0 16px; padding: 0 16px 13px; }
.pending-ship-list button { min-width: 0; padding: 8px; border: 0; border-top: 1px solid var(--border); background: transparent; color: var(--text); font: inherit; display: grid; grid-template-columns: 1fr auto; text-align: left; gap: 1px 8px; cursor: pointer; }
.pending-ship-list small { grid-row: 2; color: var(--text-3); }
.pending-ship-list b { grid-column: 2; grid-row: 1/3; align-self: center; }
.statement-range { display: flex; align-items: center; gap: 6px; color: var(--text-3); font-size: 12px; }
.statement-range .input { width: 135px; min-height: 36px; }
.owner-layout { display: grid; grid-template-columns: 300px minmax(0,1fr); gap: 14px; align-items: start; }
.owner-picker { overflow: hidden; }
.owner-search { padding: 0 12px 10px; }
.owner-list { max-height: 610px; overflow: auto; }
.owner-list > button { width: 100%; min-height: 58px; padding: 8px 12px; border: 0; border-top: 1px solid var(--border); background: transparent; display: flex; align-items: center; gap: 10px; text-align: left; cursor: pointer; }
.owner-list > button:hover, .owner-list > button.on { background: var(--brand-soft); }
.owner-avatar { width: 34px; height: 34px; flex: 0 0 auto; border-radius: 11px; display: grid; place-items: center; background: var(--surface-2); color: var(--text-2); font-weight: 800; }
.owner-list > button.on .owner-avatar { background: var(--brand); color: #4a3600; }
.owner-name { flex: 1; min-width: 0; display: grid; }
.owner-name small, .owner-money small { color: var(--text-3); font-size: 10.5px; }
.owner-money { text-align: right; display: grid; }
.statement-sheet {
  min-height: 540px;
  padding: 24px;
  overflow: hidden;
  background:
    radial-gradient(circle at 100% 0%, color-mix(in srgb, var(--brand-soft) 48%, transparent), transparent 30%),
    var(--surface);
}
.statement-print { position: relative; }
.statement-head {
  position: relative;
  display: flex;
  justify-content: space-between;
  gap: 18px;
  padding: 18px 19px;
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--brand-deep) 22%, var(--border));
  border-radius: 15px;
  background:
    linear-gradient(116deg,
      color-mix(in srgb, var(--brand-soft) 62%, var(--surface)) 0%,
      var(--surface) 58%,
      color-mix(in srgb, var(--blue-soft) 32%, var(--surface)) 100%);
}
.statement-head::after {
  content: "";
  position: absolute;
  right: -45px;
  bottom: -75px;
  width: 150px;
  height: 150px;
  border-radius: 50%;
  background: color-mix(in srgb, var(--brand-soft) 62%, transparent);
  pointer-events: none;
}
.statement-title { position: relative; z-index: 1; min-width: 0; }
.statement-title .ops-eyebrow { color: var(--brand-ink); letter-spacing: .12em; }
.statement-head h2 { margin: 6px 0 4px; font-size: 23px; line-height: 1.25; letter-spacing: -.3px; }
.statement-head p { margin: 0; color: var(--text-2); font-size: 11.5px; }
.statement-actions { display: flex; align-items: flex-start; gap: 7px; }
.statement-actions { position: relative; z-index: 1; }
.statement-totals { padding: 16px 0; display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 9px; }
.statement-totals > div,
.statement-total {
  min-width: 0;
  padding: 11px 12px 10px;
  border: 1px solid var(--border);
  border-radius: 11px;
  background: var(--surface-2);
  display: grid;
  gap: 2px;
}
.statement-total.is-period {
  border-color: color-mix(in srgb, var(--brand-deep) 26%, var(--border));
  background: color-mix(in srgb, var(--brand-soft) 46%, var(--surface));
}
.statement-total.is-pending {
  border-color: color-mix(in srgb, var(--up) 22%, var(--border));
  background: color-mix(in srgb, var(--up-soft) 44%, var(--surface));
}
.statement-totals span { color: var(--text-3); font-size: 10.5px; }
.statement-totals b { font-size: 17px; }
.statement-totals small { color: var(--text-3); font-size: 9.5px; }
.statement-table {
  overflow-x: auto;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
}
.statement-table table { width: 100%; border-collapse: collapse; font-size: 11.5px; }
.statement-table th {
  padding: 9px 10px;
  color: var(--text-3);
  text-align: left;
  border-bottom: 1px solid var(--border-strong);
  background: color-mix(in srgb, var(--surface-2) 76%, var(--surface));
  white-space: nowrap;
}
.statement-table td { padding: 9px 10px; border-bottom: 1px solid var(--border); }
.statement-table tbody tr:nth-child(even) { background: color-mix(in srgb, var(--surface-2) 52%, var(--surface)); }
.statement-table tbody tr:hover { background: var(--blue-soft); }
.statement-table tbody tr:last-child td { border-bottom: 0; }
.statement-table td small { display: block; color: var(--text-3); }
.statement-table .ta-r { text-align: right; }
.statement-history {
  margin-top: 17px;
  padding: 12px 13px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: color-mix(in srgb, var(--surface-2) 52%, var(--surface));
}
.statement-history h3 { margin: 0 0 7px; font-size: 13px; }
.statement-history > div > div { padding: 6px 0; display: flex; justify-content: space-between; border-bottom: 1px dashed var(--border); font-size: 11.5px; }
.statement-history p { margin: 0; font-size: 12px; }
.statement-foot { margin-top: 20px; padding-top: 11px; border-top: 1px solid var(--border); color: var(--text-3); font-size: 10.5px; text-align: center; }
.lot-grid { display: grid; grid-template-columns: repeat(auto-fit,minmax(250px,1fr)); gap: 11px; }
.lot-card { padding: 15px; display: flex; flex-direction: column; gap: 8px; }
.lot-card.sold { opacity: .7; }
.lot-head, .lot-foot, .pricing-head, .pricing-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.lot-head > span:last-child { font-size: 11px; }
.lot-card h3 { margin: 0; font-size: 14px; overflow-wrap: anywhere; }
.lot-card > p { margin: -5px 0 0; color: var(--text-3); font-size: 11px; }
.lot-money { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.lot-money > div, .price-grid > div { padding: 8px; border-radius: 9px; background: var(--surface-2); display: grid; }
.lot-money span, .price-grid span { color: var(--text-3); font-size: 10px; }
.lot-bar { height: 5px; overflow: hidden; border-radius: 999px; background: var(--border); }
.lot-bar i { display: block; height: 100%; border-radius: inherit; background: linear-gradient(90deg,var(--brand),var(--brand-deep)); }
.lot-foot { color: var(--text-3); font-size: 10.5px; }
.lot-edit { padding-top: 8px; border-top: 1px dashed var(--border); display: grid; gap: 6px; }
.lot-edit > div { display: flex; justify-content: flex-end; gap: 6px; }
.pricing-controls { display: flex; align-items: center; gap: 12px; min-width: 260px; }
.pricing-controls label { display: grid; gap: 2px; font-size: 10.5px; color: var(--text-3); white-space: nowrap; }
.pricing-controls label b { color: var(--text); font-size: 13px; }
.pricing-controls input[type="range"] { accent-color: var(--brand-deep); width: 140px; }
.pricing-note { padding: 12px 14px; display: flex; align-items: center; gap: 10px; }
.pricing-note span { color: var(--brand-ink); font-size: 11px; font-weight: 800; white-space: nowrap; }
.pricing-note p { flex: 1; margin: 0; color: var(--text-3); font-size: 11px; }
.pricing-note .input { width: 190px; }
.pricing-grid { display: grid; grid-template-columns: repeat(auto-fit,minmax(270px,1fr)); gap: 11px; }
.pricing-card { padding: 15px; display: flex; flex-direction: column; gap: 10px; border-top: 3px solid var(--border); }
.pricing-card.restock { border-top-color: var(--down); background: linear-gradient(160deg,var(--surface),var(--down-soft)); }
.pricing-card h3 { margin: 0; font-size: 14px; }
.price-main { display: grid; grid-template-columns: 1fr auto; align-items: end; }
.price-main span { color: var(--text-3); font-size: 10.5px; }
.price-main b { grid-row: 1/3; grid-column: 2; font-size: 27px; line-height: 1; color: var(--brand-ink); }
.price-main small { color: var(--text-3); font-size: 10.5px; }
.price-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
.pricing-reason { margin: 0; color: var(--text-3); font-size: 11px; min-height: 32px; }
.pricing-foot > span { color: var(--text-3); font-size: 10px; }
.shop-cockpit { display: grid; grid-template-columns: repeat(auto-fit,minmax(285px,1fr)); gap: 11px; }
.cockpit-card { padding: 16px; display: flex; flex-direction: column; gap: 10px; cursor: pointer; transition: .18s; }
.cockpit-card:hover { transform: translateY(-2px); box-shadow: var(--shadow-lg); border-color: rgba(245,181,0,.35); }
.cockpit-head { display: flex; align-items: center; gap: 8px; }
.cockpit-head i { width: 9px; height: 26px; border-radius: 999px; }
.cockpit-head b { flex: 1; }
.cockpit-head > span { font-size: 20px; font-weight: 800; }
.cockpit-bar { height: 6px; border-radius: 999px; background: var(--border); overflow: hidden; }
.cockpit-bar i { display: block; height: 100%; border-radius: inherit; background: linear-gradient(90deg,var(--brand),var(--brand-deep)); }
.cockpit-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 7px; }
.cockpit-grid > div { padding: 8px; background: var(--surface-2); border-radius: 9px; display: grid; }
.cockpit-grid span, .cockpit-card > small { color: var(--text-3); font-size: 10.5px; }
.ops-loading { padding: 34px; display: flex; align-items: center; justify-content: center; gap: 10px; color: var(--text-2); }
.ops-skeleton { display: grid; gap: 9px; }
.ops-skeleton.compact { padding: 12px; }
.ops-skel { min-height: 74px; }

/* 待办、审计与质量趋势 */
.todo-overview { display: grid; grid-template-columns: minmax(260px, 1.45fr) repeat(3, minmax(150px, 1fr)); gap: 10px; }
.todo-overview > .card { min-height: 112px; padding: 15px 16px; }
.todo-score { display: flex; align-items: center; gap: 15px; background: linear-gradient(145deg, var(--surface), var(--surface-2)); }
.todo-score-ring {
  --ring: var(--down);
  position: relative; flex: 0 0 auto; width: 70px; height: 70px; display: grid; place-content: center;
  text-align: center; border-radius: 50%;
  background: conic-gradient(var(--ring) var(--score), var(--border) 0);
}
.todo-score-ring::after { content: ""; position: absolute; inset: 6px; border-radius: inherit; background: var(--surface); }
.todo-score-ring b, .todo-score-ring small { position: relative; z-index: 1; }
.todo-score-ring b { font-size: 22px; line-height: 1; }
.todo-score-ring small { color: var(--text-3); font-size: 9px; }
.todo-score.warn { --ring: #d99410; }
.todo-score.bad { --ring: var(--up); }
.todo-score > div:last-child, .todo-kpi { display: flex; flex-direction: column; justify-content: center; gap: 4px; }
.todo-score span, .todo-kpi span { color: var(--text-3); font-size: 10.5px; font-weight: 700; }
.todo-score > div:last-child > b { font-size: 16px; }
.todo-score small, .todo-kpi small { color: var(--text-3); font-size: 10.5px; }
.todo-kpi b { font-size: 24px; line-height: 1.15; letter-spacing: -.5px; }
.todo-kpi .todo-time { font-size: 14px; letter-spacing: 0; line-height: 1.35; }
.todo-list { display: grid; gap: 9px; }
.todo-item {
  --tone: var(--text-3);
  padding: 14px 16px; display: grid; grid-template-columns: 8px minmax(0, 1fr) auto; align-items: center; gap: 13px;
  animation: opsIn .32s ease both; animation-delay: var(--delay); border-left: 3px solid var(--tone);
}
.todo-item.lv-danger { --tone: var(--up); }
.todo-item.lv-warn { --tone: #d99410; }
.todo-item.lv-info { --tone: var(--blue); }
.todo-severity { width: 8px; height: 8px; border-radius: 50%; background: var(--tone); box-shadow: 0 0 0 5px color-mix(in srgb, var(--tone) 13%, transparent); }
.todo-copy { min-width: 0; }
.todo-copy > div { display: flex; align-items: center; gap: 8px; }
.todo-copy b { font-size: 13.5px; }
.todo-copy p { margin: 4px 0 0; color: var(--text-2); font-size: 11.5px; line-height: 1.5; }
.todo-copy > small { display: block; margin-top: 4px; color: var(--text-3); font-size: 10.5px; }
.todo-count { padding: 2px 7px; border-radius: 999px; background: var(--surface-2); color: var(--text-3); font-size: 10px; font-weight: 700; }
.todo-action { min-width: 88px; }
.todo-alert { padding: 14px 16px; display: flex; align-items: center; justify-content: space-between; gap: 16px; border-color: rgba(197,60,38,.24); background: linear-gradient(135deg,var(--surface),var(--up-soft)); }
.todo-alert b { color: var(--up); font-size: 12.5px; }
.todo-alert p { margin: 4px 0 0; color: var(--text-2); font-size: 11.5px; overflow-wrap: anywhere; }
.todo-generated { margin: -4px 2px 0; color: var(--text-3); font-size: 10.5px; text-align: right; }
.todo-clean { min-height: 190px; }

.audit-toolbar { padding: 12px; display: grid; grid-template-columns: minmax(0,1fr) 140px auto; gap: 8px; }
.audit-search { position: relative; display: flex; align-items: center; }
.audit-search svg { position: absolute; left: 12px; color: var(--text-3); pointer-events: none; }
.audit-search .input { width: 100%; padding-left: 36px; }
.audit-list { display: grid; gap: 7px; }
.audit-row { overflow: hidden; transition: border-color .18s ease, box-shadow .18s ease; }
.audit-row.open { border-color: color-mix(in srgb, var(--brand-deep) 34%, var(--border)); box-shadow: var(--shadow-sm); }
.audit-summary { width: 100%; min-height: 66px; padding: 11px 14px; border: 0; background: transparent; color: var(--text); display: grid; grid-template-columns: 150px minmax(0,1fr) auto; align-items: center; gap: 14px; text-align: left; cursor: pointer; }
.audit-summary:hover { background: var(--surface-2); }
.audit-time { display: grid; gap: 2px; padding-right: 12px; border-right: 1px solid var(--border); }
.audit-time b { font-size: 11px; font-weight: 650; color: var(--text-2); white-space: nowrap; }
.audit-time small, .audit-main-copy small { color: var(--text-3); font-size: 10px; }
.audit-main-copy { min-width: 0; display: grid; gap: 3px; }
.audit-main-copy b { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; }
.audit-open-mark { display: inline-flex; align-items: center; gap: 4px; color: var(--text-3); font-size: 10.5px; }
.audit-row.open .audit-open-mark svg { transform: rotate(180deg); }
.audit-open-mark svg { transition: transform .18s ease; }
.audit-detail { padding: 0 14px 13px 178px; }
.audit-detail > p { margin: 0 0 9px; color: var(--text-3); font-size: 11px; }
.audit-changes { display: grid; gap: 5px; margin-bottom: 9px; }
.audit-changes > div { display: grid; grid-template-columns: 120px minmax(0,.8fr) minmax(0,1fr); gap: 8px; align-items: center; padding: 7px 9px; border-radius: 8px; background: var(--surface-2); font-size: 10.5px; }
.audit-changes span { color: var(--text-3); font-weight: 700; }
.audit-changes del { color: var(--up); overflow-wrap: anywhere; }
.audit-changes b { color: var(--down); overflow-wrap: anywhere; }

.quality-kpis .warn-text { color: #aa6b00; }
html[data-theme="dark"] .quality-kpis .warn-text { color: #fbbf24; }
.quality-chart { padding: 16px 18px 12px; overflow: hidden; }
.quality-chart-skeleton { height: 260px; }
.quality-chart-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 8px; }
.quality-chart-head > div { display: grid; gap: 3px; }
.quality-chart-head b { font-size: 13.5px; }
.quality-chart-head span { color: var(--text-3); font-size: 10.5px; }
.quality-legend { display: inline-flex; align-items: center; gap: 5px; }
.quality-legend i { width: 9px; height: 9px; border-radius: 50%; background: var(--brand); }
.quality-chart svg { display: block; width: 100%; height: auto; min-height: 150px; max-height: 240px; overflow: visible; }
.quality-grid path { stroke: var(--border); stroke-width: 1; stroke-dasharray: 3 5; }
.quality-grid text { fill: var(--text-3); font-size: 9px; }
.quality-line { fill: none; stroke: var(--brand-deep); stroke-width: 2.5; stroke-linecap: round; stroke-linejoin: round; filter: drop-shadow(0 3px 5px rgba(245,181,0,.22)); }
.quality-dot circle { fill: var(--surface); stroke: var(--brand-deep); stroke-width: 2.5; }
.quality-dot:hover circle { fill: var(--brand); r: 6; }
.quality-chart-foot { display: flex; justify-content: space-between; gap: 10px; color: var(--text-3); font-size: 10px; }
.quality-chart-foot b { color: var(--text-2); font-weight: 650; }
.quality-history { overflow: hidden; }
.quality-history-list > div { min-height: 39px; padding: 8px 16px; display: grid; grid-template-columns: 120px 76px minmax(0,1fr) auto; align-items: center; gap: 10px; border-top: 1px solid var(--border); font-size: 11px; }
.quality-history-list > div:first-child { border-top: 0; }
.quality-history-list small { color: var(--text-3); }
.quality-history-list em { font-style: normal; color: var(--text-2); font-size: 11px; }

@media (max-width: 1000px) {
  .ops-stats, .ops-stats.five { grid-template-columns: repeat(2,minmax(0,1fr)); }
  .owner-layout { grid-template-columns: 1fr; }
  .owner-list { max-height: 300px; }
  .todo-overview { grid-template-columns: repeat(2,minmax(0,1fr)); }
  .audit-detail { padding-left: 14px; }
}
@media (max-width: 650px) {
  .ops-hero { padding: 18px; align-items: flex-start; flex-direction: column; gap: 14px; }
  .ops-hero h2 { font-size: 20px; }
  .ops-score { width: 100%; min-width: 0; padding: 10px 13px; }
  .ops-score-num { font-size: 30px; }
  .ops-tabs { margin-inline: -12px; padding-inline: 12px; }
  .ops-tabs button { min-height: 44px; padding: 0 12px; }
  .ops-tabs button span { font-size: 12px; }
  .ops-section-head { align-items: flex-start; flex-direction: column; }
  .ops-actions { width: 100%; justify-content: stretch; }
  .ops-actions .btn { flex: 1; }
  .health-top { flex-wrap: wrap; align-items: flex-start; }
  .health-copy { flex-basis: calc(100% - 28px); }
  .health-amount { margin-left: 24px; text-align: left; }
  .health-top > .btn { margin-left: auto; }
  .health-items { margin-left: 0; grid-template-columns: 1fr; }
  .ops-stats, .ops-stats.five { grid-template-columns: 1fr 1fr; }
  .ops-stats b { font-size: 19px; }
  .transport-drop { padding: 14px; justify-content: flex-start; }
  .transport-preview-head .btn { width: 100%; margin-left: 0; justify-content: center; }
  .statement-range { width: 100%; flex-wrap: wrap; }
  .statement-range .input { flex: 1; min-width: 120px; width: auto; }
  .statement-sheet { padding: 15px; }
  .statement-head { flex-direction: column; }
  .statement-actions { width: 100%; }
  .statement-actions .btn { flex: 1; }
  .statement-totals { grid-template-columns: 1fr 1fr; }
  .pricing-controls { min-width: 0; width: 100%; justify-content: space-between; }
  .pricing-note { flex-wrap: wrap; }
  .pricing-note .input { width: 100%; }
  .ops-table { min-width: 720px; }
  .todo-overview { grid-template-columns: 1fr 1fr; }
  .todo-score { grid-column: 1 / -1; }
  .todo-overview > .card { min-height: 92px; padding: 12px; }
  .todo-kpi b { font-size: 19px; }
  .todo-kpi .todo-time { font-size: 11.5px; }
  .todo-item { grid-template-columns: 7px minmax(0,1fr); padding: 13px; }
  .todo-action { grid-column: 2; justify-self: start; min-height: 44px; }
  .todo-alert { align-items: flex-start; flex-direction: column; }
  .todo-alert .btn { min-height: 44px; }
  .audit-toolbar { grid-template-columns: 1fr 110px; }
  .audit-search { grid-column: 1 / -1; }
  .audit-toolbar .btn { min-height: 44px; }
  .audit-summary { min-height: 76px; grid-template-columns: 1fr auto; gap: 8px; padding: 11px 12px; }
  .audit-time { grid-column: 1 / -1; grid-template-columns: 1fr auto; padding: 0 0 6px; border-right: 0; border-bottom: 1px solid var(--border); }
  .audit-time small { text-align: right; }
  .audit-main-copy b { white-space: normal; }
  .audit-open-mark { min-width: 44px; min-height: 44px; justify-content: flex-end; }
  .audit-changes > div { grid-template-columns: 1fr; gap: 3px; }
  .quality-chart { padding: 13px 11px 10px; }
  .quality-chart svg { min-height: 130px; }
  .quality-history-list > div { grid-template-columns: 94px 66px 1fr; padding-inline: 12px; }
  .quality-history-list em { grid-column: 2 / -1; }
}
@media print {
  :global(body) { background: #fff; }
  :global(.sidebar), :global(.topbar), :global(.tabbar), :global(.site-footer),
  :global(.to-top), .ops-hero, .ops-tabs, .ops-section-head, .owner-picker,
  .ops-error, .ops-nonprint { display: none !important; }
  :global(.main-body) { max-width: none !important; margin: 0 !important; padding: 0 !important; }
  :global(.main), :global(.app-shell) { display: block !important; margin: 0 !important; padding: 0 !important; }
  .ops-page { display: block; }
  .ops-section { display: block; }
  .owner-layout { display: block; }
  .statement-sheet { border: 0; box-shadow: none; min-height: 0; padding: 0; }
  .statement-print {
    --surface: #fff;
    --surface-2: #f7f7f5;
    --text: #171717;
    --text-2: #363636;
    --text-3: #666;
    --border: #deded8;
    --border-strong: #c9c9c1;
    --brand-soft: #fff8e2;
    --brand-deep: #c18b00;
    --brand-ink: #6b5100;
    --blue-soft: #eef3ff;
    --up-soft: #eaf7ef;
    --up: #146b3b;
    color: #111;
    color-scheme: light;
  }
  .statement-head,
  .statement-total.is-period,
  .statement-total.is-pending {
    -webkit-print-color-adjust: exact;
    print-color-adjust: exact;
  }
  .statement-table tr { break-inside: avoid; }
}
</style>
