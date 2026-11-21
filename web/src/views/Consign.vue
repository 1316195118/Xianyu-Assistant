<script setup>
import { ref, computed, watch, nextTick, onMounted, onActivated, onDeactivated, onBeforeUnmount, defineAsyncComponent } from 'vue'
import { api } from '../api'
import { confirm } from '../confirm'
import { maskCloser } from '../maskClose'
import { fmtMoney, fmtNum } from '../utils'

const RecordModal = defineAsyncComponent(() => import('../components/RecordModal.vue'))

// 寄卖账本：别人的货放在这卖（寄卖），或垫钱替别人买（代采），都按归属人分账、可结算。
// 这两类单都不计入首页利润 / 成本（后端按 source 排除），只在这里统计。
const props = defineProps({
  refreshKey: { type: Number, default: 0 },
  initialTab: { type: String, default: 'sell' },
  focusNonce: { type: Number, default: 0 }
})
const emit = defineEmits(['notify'])

const data = ref(null)
const err = ref('')
const loading = ref(true)
const loadedAt = ref(null)
const q = ref('')
const searchOpen = ref(false)
const searchIndex = ref(0)
const filterOpen = ref(false)
const active = ref('')
const from = ref('')
const to = ref('')
const rangeKey = ref('month')
const tab = ref('sell') // sell=寄卖（我该付给寄卖人） buy=代采（归属人该还我）

const settleOpen = ref(false)
const settleOwner = ref(null)
const settleAmount = ref(0)
const settleNote = ref('')
const settleBusy = ref(false)

const historyOpen = ref(false)
const historyOwner = ref('')
const historyKind = ref('sell')
const history = ref([])
const historyLoading = ref(false)
const historyErr = ref('')

const assignOpen = ref(false)
const assignTarget = ref(null)
const assignName = ref('')
const assignNames = ref([])
const assignBusy = ref(false)
const assignErr = ref('')

const itemDetail = ref(null)
const itemLoadingId = ref(0)

const settleDialog = ref(null)
const historyDialog = ref(null)
const assignDialog = ref(null)
let settleReturnFocus = null
let historyReturnFocus = null
let assignReturnFocus = null

let loadSeq = 0
let settingRange = false
const settleMask = maskCloser(closeSettle)
const historyMask = maskCloser(closeHistory)
const assignMask = maskCloser(closeAssign)

const fmtDate = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
const round2 = (n) => Math.round((Number(n) || 0) * 100) / 100
const isPending = (n) => Number(n) > 0.005

function applyRange(key) {
  settingRange = true
  rangeKey.value = key
  const now = new Date()
  if (key === 'month') {
    from.value = fmtDate(new Date(now.getFullYear(), now.getMonth(), 1))
    to.value = fmtDate(now)
  } else if (key === 'lastMonth') {
    const start = new Date(now.getFullYear(), now.getMonth() - 1, 1)
    const end = new Date(now.getFullYear(), now.getMonth(), 0)
    from.value = fmtDate(start)
    to.value = fmtDate(end)
  } else if (key === 'quarter') {
    const quarterMonth = Math.floor(now.getMonth() / 3) * 3
    from.value = fmtDate(new Date(now.getFullYear(), quarterMonth, 1))
    to.value = fmtDate(now)
  } else if (key === 'year') {
    from.value = fmtDate(new Date(now.getFullYear(), 0, 1))
    to.value = fmtDate(now)
  } else {
    from.value = ''
    to.value = ''
  }
  setTimeout(() => {
    settingRange = false
    load()
  }, 0)
}

async function load() {
  const seq = ++loadSeq
  if (!data.value) loading.value = true
  try {
    const params = {}
    if (from.value) params.from = from.value
    if (to.value) params.to = to.value
    const result = await api.consignStats(params)
    if (seq !== loadSeq) return
    data.value = result
    loadedAt.value = new Date()
    err.value = ''
  } catch (e) {
    if (seq !== loadSeq) return
    err.value = e.message || '加载失败'
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

onMounted(() => {
  document.addEventListener('keydown', onOverlayKeydown)
  if (props.initialTab === 'buy' || props.initialTab === 'sell') tab.value = props.initialTab
  applyRange('month')
})
onActivated(() => {
  if (!loadedAt.value || Date.now() - loadedAt.value.getTime() > 800) load()
})
onDeactivated(() => {
  settleOpen.value = false
  historyOpen.value = false
  assignOpen.value = false
  itemDetail.value = null
  document.body.classList.remove('overlay-open')
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onOverlayKeydown)
  document.body.classList.remove('overlay-open')
})
watch(() => props.refreshKey, load)
watch([settleOpen, historyOpen, assignOpen, itemDetail], ([settling, viewingHistory, assigning, viewingItem]) => {
  document.body.classList.toggle('overlay-open', settling || viewingHistory || assigning || !!viewingItem)
})
watch([from, to], (value, old) => {
  if (!settingRange && old && (value[0] !== old[0] || value[1] !== old[1])) rangeKey.value = 'custom'
  if (!settingRange) load()
})

const isBuy = computed(() => tab.value === 'buy')
const total = computed(() => data.value?.total || {})
const historyOrders = computed(() => Number(isBuy.value ? total.value.buy_history_orders : total.value.history_orders) || 0)
const historyLatest = computed(() => String((isBuy.value ? total.value.buy_history_latest : total.value.history_latest) || ''))
const rawList = computed(() => {
  if (!data.value) return []
  return (isBuy.value ? data.value.buyers : data.value.owners) || []
})

function normalizeOwnerSearch(value) {
  return String(value || '').toLocaleLowerCase('zh-CN').replace(/[\s·•._\-，,。]/g, '')
}

function editDistance(a, b) {
  const left = Array.from(a)
  const right = Array.from(b)
  const prev = Array.from({ length: right.length + 1 }, (_, i) => i)
  for (let i = 1; i <= left.length; i++) {
    let diagonal = prev[0]
    prev[0] = i
    for (let j = 1; j <= right.length; j++) {
      const above = prev[j]
      prev[j] = Math.min(prev[j] + 1, prev[j - 1] + 1, diagonal + (left[i - 1] === right[j - 1] ? 0 : 1))
      diagonal = above
    }
  }
  return prev[right.length]
}

function ownerMatchScore(name, keyword) {
  const source = normalizeOwnerSearch(name)
  const key = normalizeOwnerSearch(keyword)
  if (!key) return 0
  if (!source) return -1
  if (source === key) return 1000
  if (source.startsWith(key)) return 900 - Math.max(0, source.length - key.length)
  const at = source.indexOf(key)
  if (at >= 0) return 760 - at * 12 - Math.max(0, source.length - key.length)

  let cursor = 0
  for (const ch of source) {
    if (ch === key[cursor]) cursor++
    if (cursor === key.length) break
  }
  if (cursor === key.length) return 560 - Math.max(0, source.length - key.length) * 8

  const distance = editDistance(source, key)
  const tolerance = key.length <= 2 ? 1 : Math.max(1, Math.floor(key.length / 3))
  return distance <= tolerance ? 420 - distance * 80 - Math.abs(source.length - key.length) * 8 : -1
}

const allOwnerNames = computed(() => {
  const names = isBuy.value ? data.value?.buyer_names : data.value?.owner_names
  return Array.isArray(names) ? names : []
})

const ownerChoices = computed(() => {
  const rangeMap = new Map(rawList.value.map((owner) => [owner.owner, owner]))
  const order = new Map(allOwnerNames.value.map((name, index) => [name, index]))
  const names = [...new Set([...allOwnerNames.value, ...rawList.value.map((owner) => owner.owner)])]
    .filter((name) => name && name !== '未指定归属')
  const keyword = q.value.trim()
  return names
    .map((name) => {
      const current = rangeMap.get(name)
      return {
        owner: name,
        inRange: !!current,
        orders: Number(current?.orders) || 0,
        pending: Number(current?.pending) || 0,
        score: ownerMatchScore(name, keyword),
        order: order.get(name) ?? Number.MAX_SAFE_INTEGER
      }
    })
    .filter((item) => !keyword || item.score >= 0)
    .sort((a, b) => {
      if (!!b.inRange !== !!a.inRange) return Number(b.inRange) - Number(a.inRange)
      if (keyword && b.score !== a.score) return b.score - a.score
      if (!keyword && a.order !== b.order) return a.order - b.order
      if (b.pending !== a.pending) return b.pending - a.pending
      return a.owner.localeCompare(b.owner, 'zh-CN')
    })
    .slice(0, 8)
})

const list = computed(() => {
  const keyword = q.value.trim()
  const filtered = keyword
    ? rawList.value.filter((owner) => ownerMatchScore(owner.owner, keyword) >= 0)
    : [...rawList.value]
  return filtered.sort((a, b) => {
    const pendingOrder = Number(isPending(b.pending)) - Number(isPending(a.pending))
    if (pendingOrder) return pendingOrder
    const pendingDiff = (Number(b.pending) || 0) - (Number(a.pending) || 0)
    if (pendingDiff) return pendingDiff
    return String(a.owner || '').localeCompare(String(b.owner || ''), 'zh-CN')
  })
})
const shown = computed(() => active.value ? list.value.filter((owner) => owner.owner === active.value) : list.value)
const unresolvedCount = computed(() => rawList.value.filter((owner) => isPending(owner.pending)).length)

const ledgerTabs = computed(() => {
  const t = total.value
  return [
    {
      key: 'sell',
      title: '寄卖',
      desc: '我替别人卖',
      people: (data.value?.owners || []).length,
      balance: t.pending || 0,
      pendingLabel: '待结'
    },
    {
      key: 'buy',
      title: '代采',
      desc: '我垫钱替人买',
      people: (data.value?.buyers || []).length,
      balance: t.buy_pending || 0,
      pendingLabel: '待收'
    }
  ]
})

const summary = computed(() => {
  const t = total.value
  if (isBuy.value) {
    return {
      eyebrow: '垫付往来',
      title: '代采垫付',
      intro: '先替委托人买单，再按人收回垫付款。',
      pendingLabel: '累计待收',
      pending: t.buy_pending || 0,
      metrics: [
        { label: '区间垫付', value: fmtMoney(t.buy_amount || 0) },
        { label: '区间单数', value: `${fmtNum(t.buy_orders || 0)} 单` },
        { label: '累计已收', value: fmtMoney(t.buy_settled || 0) },
        { label: '服务人数', value: `${fmtNum(data.value?.buy_service_people || 0)} 人` }
      ]
    }
  }
  return {
    eyebrow: '代卖往来',
    title: '寄卖分账',
    intro: '货是别人的，卖出后按人结清应付款。',
    pendingLabel: '累计待结',
    pending: t.pending || 0,
    metrics: [
      { label: '区间销售额', value: fmtMoney(t.sales || 0) },
      { label: '区间单数', value: `${fmtNum(t.orders || 0)} 单` },
      { label: '累计已结', value: fmtMoney(t.settled || 0) },
      { label: '服务人数', value: `${fmtNum(data.value?.service_people || 0)} 人` }
    ]
  }
})

const rangeText = computed(() => {
  const labels = { month: '本月', lastMonth: '上月', quarter: '本季度', year: '今年', all: '全部时间' }
  if (rangeKey.value !== 'custom') return labels[rangeKey.value] || '自选日期'
  if (from.value && to.value) return `${from.value} 至 ${to.value}`
  if (from.value) return `${from.value} 起`
  if (to.value) return `截至 ${to.value}`
  return '全部时间'
})
const activeFilterCount = computed(() => (
  Number(!!(q.value.trim() || active.value)) + Number(rangeKey.value !== 'month')
))
const filterSummary = computed(() => [
  `${list.value.length} / ${rawList.value.length} 人`,
  rangeText.value,
  active.value || q.value.trim()
].filter(Boolean).join(' · '))

const loadedText = computed(() => {
  if (!loadedAt.value) return ''
  return loadedAt.value.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
})

const settlePending = computed(() => Number(settleOwner.value?.pending) || 0)
const settleRemaining = computed(() => Math.max(0, settlePending.value - (Number(settleAmount.value) || 0)))
const historyTotal = computed(() => round2(history.value.reduce((sum, row) => sum + (Number(row.amount) || 0), 0)))

function switchTab(key) {
  if (tab.value === key) return
  tab.value = key
  active.value = ''
  q.value = ''
  searchOpen.value = false
}
watch(() => props.focusNonce, () => {
  if (props.initialTab === 'buy' || props.initialTab === 'sell') switchTab(props.initialTab)
})

function resetFilters() {
  q.value = ''
  active.value = ''
  applyRange('month')
}

function clearSearch() {
  q.value = ''
  searchIndex.value = 0
  searchOpen.value = true
}

function pickOwner(name) {
  active.value = active.value === name ? '' : name
}

function onSearchFocus() {
  searchOpen.value = true
  searchIndex.value = 0
}

function onSearchInput() {
  searchOpen.value = true
  searchIndex.value = 0
}

function closeSearchSoon() {
  setTimeout(() => {
    searchOpen.value = false
  }, 120)
}

function selectSearchOwner(owner) {
  q.value = owner
  active.value = owner
  searchOpen.value = false
  searchIndex.value = 0
  if (!rawList.value.some((row) => row.owner === owner)) applyRange('all')
}

function onSearchKeydown(event) {
  if (event.key === 'Escape') {
    searchOpen.value = false
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    searchOpen.value = true
    if (ownerChoices.value.length) {
      searchIndex.value = (searchIndex.value + 1) % ownerChoices.value.length
    }
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    searchOpen.value = true
    if (ownerChoices.value.length) {
      searchIndex.value = (searchIndex.value - 1 + ownerChoices.value.length) % ownerChoices.value.length
    }
    return
  }
  if (event.key === 'Enter' && searchOpen.value && ownerChoices.value.length) {
    event.preventDefault()
    selectSearchOwner(ownerChoices.value[Math.min(searchIndex.value, ownerChoices.value.length - 1)].owner)
  }
}

function initial(name) {
  return Array.from(String(name || '?'))[0] || '?'
}

function statusText(owner) {
  if (!isPending(owner.pending)) return '已结清'
  return isBuy.value ? '待收款' : '待结算'
}

function settleProgress(owner) {
  const settled = Number(owner.settled) || 0
  const pending = Number(owner.pending) || 0
  const base = settled + pending
  return base > 0 ? Math.min(100, Math.max(0, (settled / base) * 100)) : 0
}

function previewItems(owner) {
  return (owner.items || []).filter((item) => item.product).slice(0, 2)
}

function dayOf(value) {
  return String(value || '').slice(0, 10) || '—'
}

function setSettleAmount(ratio) {
  settleAmount.value = ratio === 'all'
    ? settlePending.value
    : round2(settlePending.value * ratio)
}

function rememberFocus(target) {
  return target instanceof HTMLElement ? target : document.activeElement instanceof HTMLElement ? document.activeElement : null
}

function restoreFocus(target) {
  nextTick(() => {
    if (target?.isConnected) target.focus()
  })
}

function openSettle(owner) {
  settleReturnFocus = rememberFocus(document.activeElement)
  settleOwner.value = owner
  settleAmount.value = Number(owner.pending) || 0
  settleNote.value = ''
  settleOpen.value = true
  nextTick(() => settleDialog.value?.querySelector('#cs-settle-amount')?.focus())
}

function closeSettle() {
  if (!settleOpen.value) return
  settleOpen.value = false
  restoreFocus(settleReturnFocus)
}

function closeHistory() {
  if (!historyOpen.value) return
  historyOpen.value = false
  restoreFocus(historyReturnFocus)
}

function closeAssign() {
  if (!assignOpen.value) return
  assignOpen.value = false
  assignErr.value = ''
  restoreFocus(assignReturnFocus)
}

function onOverlayKeydown(event) {
  if (document.querySelector('.confirm-mask')) return
  const dialog = settleOpen.value
    ? settleDialog.value
    : historyOpen.value
      ? historyDialog.value
      : assignOpen.value
        ? assignDialog.value
        : null
  if (!dialog) return

  if (event.key === 'Escape') {
    event.preventDefault()
    if (settleOpen.value) closeSettle()
    else if (historyOpen.value) closeHistory()
    else closeAssign()
    return
  }
  if (event.key !== 'Tab') return

  const focusable = [...dialog.querySelectorAll('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])')]
    .filter((el) => el.getClientRects().length > 0)
  if (!focusable.length) {
    event.preventDefault()
    dialog.focus()
    return
  }

  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const activeInside = dialog.contains(document.activeElement)
  if (event.shiftKey && (!activeInside || document.activeElement === first)) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (!activeInside || document.activeElement === last)) {
    event.preventDefault()
    first.focus()
  }
}

async function submitSettle() {
  if (!settleOwner.value || settleBusy.value) return
  const amount = round2(settleAmount.value)
  if (amount <= 0) {
    emit('notify', '结算金额要大于 0', true)
    return
  }
  const kind = isBuy.value ? 'buy' : 'sell'
  const name = settleOwner.value.owner
  settleBusy.value = true
  try {
    await api.consignSettle({ owner: name, kind, amount, note: settleNote.value.trim() })
    emit('notify', isBuy.value ? `已收到「${name}」还款 ${fmtMoney(amount)}` : `已给「${name}」结算 ${fmtMoney(amount)}`)
    settleOpen.value = false
    await load()
  } catch (e) {
    emit('notify', `结算失败：${e.message}`, true)
  } finally {
    settleBusy.value = false
  }
}

async function fetchHistory() {
  if (!historyOwner.value) return
  historyLoading.value = true
  historyErr.value = ''
  try {
    const result = await api.consignSettlements({ owner: historyOwner.value, kind: historyKind.value })
    history.value = result.items || []
  } catch (e) {
    historyErr.value = e.message || '读取结算记录失败'
  } finally {
    historyLoading.value = false
  }
}

async function openHistory(owner) {
  historyReturnFocus = rememberFocus(document.activeElement)
  historyOwner.value = owner.owner
  historyKind.value = isBuy.value ? 'buy' : 'sell'
  history.value = []
  historyErr.value = ''
  historyOpen.value = true
  nextTick(() => historyDialog.value?.querySelector('.modal-head .x')?.focus())
  await fetchHistory()
}

async function openAssign(owner) {
  assignReturnFocus = rememberFocus(document.activeElement)
  assignTarget.value = owner
  assignName.value = ''
  assignErr.value = ''
  assignBusy.value = false
  assignOpen.value = true
  nextTick(() => assignDialog.value?.querySelector('#cs-assign-owner')?.focus())
  try {
    assignNames.value = (await api.consignOwners()).owners || []
  } catch (e) {
    assignNames.value = []
  }
}

async function submitAssign() {
  if (assignBusy.value || !assignTarget.value) return
  const owner = assignName.value.trim()
  if (!owner) {
    assignErr.value = '请填写归属人'
    return
  }
  const itemIds = (assignTarget.value.items || []).map((item) => item.id).filter(Boolean)
  if (!itemIds.length) {
    assignErr.value = '没有可补录归属的单据'
    return
  }
  assignBusy.value = true
  assignErr.value = ''
  try {
    const result = await api.consignAssignOwner({
      owner,
      kind: isBuy.value ? 'buy' : 'sell',
      item_ids: itemIds
    })
    closeAssign()
    emit('notify', `已将 ${fmtNum(result.updated || itemIds.length)} 笔单据补到「${owner}」`)
    await load()
  } catch (e) {
    assignErr.value = e.message || '补录归属失败'
  } finally {
    assignBusy.value = false
  }
}

async function openItemDetail(item) {
  if (!item?.id || itemLoadingId.value) return
  itemLoadingId.value = item.id
  try {
    itemDetail.value = await api.record(item.id)
  } catch (e) {
    emit('notify', `读取商品详情失败：${e.message}`, true)
  } finally {
    itemLoadingId.value = 0
  }
}

function closeItemDetail() {
  itemDetail.value = null
}

function onItemSaved() {
  closeItemDetail()
  load()
}

async function delSettlement(row) {
  const buy = historyKind.value === 'buy'
  const ok = await confirm({
    title: buy ? '删除收款记录' : '删除结算记录',
    message: buy ? '删除后这笔金额会重新计入待收。' : '删除后这笔金额会重新计入待结。',
    confirmText: '删除',
    danger: true
  })
  if (!ok) return
  try {
    await api.deleteConsignSettlement(row.id)
    emit('notify', '已删除结算记录')
    history.value = history.value.filter((item) => item.id !== row.id)
    await load()
  } catch (e) {
    emit('notify', `删除失败：${e.message}`, true)
  }
}
</script>

<template>
  <div class="page consign-page" :class="{ 'is-buy': isBuy }" :aria-busy="loading">
    <div v-if="err" class="alert-err" role="alert">
      <span>{{ err }}</span>
      <button class="btn btn-sm" @click="load">重试</button>
    </div>

    <section class="cs-hero" aria-labelledby="cs-title">
      <div class="cs-ledger-switch" role="tablist" aria-label="选择往来账本">
        <button
          v-for="ledger in ledgerTabs"
          :key="ledger.key"
          class="cs-ledger-tab"
          :class="{ on: tab === ledger.key, buy: ledger.key === 'buy' }"
          role="tab"
          :aria-selected="tab === ledger.key"
          @click="switchTab(ledger.key)"
        >
          <span class="cs-ledger-copy">
            <b>{{ ledger.title }}</b>
            <small>{{ ledger.desc }}</small>
          </span>
          <span class="cs-ledger-balance">
            <small>{{ ledger.pendingLabel }}</small>
            <b class="num">{{ fmtMoney(ledger.balance) }}</b>
          </span>
        </button>
      </div>

      <div class="cs-hero-body">
        <div class="cs-hero-copy">
          <span class="cs-eyebrow">{{ summary.eyebrow }}</span>
          <h2 id="cs-title">{{ summary.title }}</h2>
          <p>{{ summary.intro }}</p>
          <div class="cs-hero-status">
            <span class="cs-range-pill">{{ rangeText }}</span>
            <span v-if="unresolvedCount" class="cs-attention">{{ unresolvedCount }} 人待处理</span>
            <span v-else class="cs-all-clear">当前没有未结往来</span>
          </div>
        </div>

        <div class="cs-balance-card">
          <span>{{ summary.pendingLabel }}</span>
          <strong class="num" :class="{ zero: !isPending(summary.pending) }">{{ fmtMoney(summary.pending) }}</strong>
          <small>{{ isBuy ? '所有代采垫付扣除已收款后的余额' : '所有寄卖到手款扣除已结算后的余额' }}</small>
        </div>
      </div>

      <div class="cs-summary-metrics">
        <div v-for="metric in summary.metrics" :key="metric.label" class="cs-summary-metric">
          <span>{{ metric.label }}</span>
          <b class="num">{{ metric.value }}</b>
        </div>
        <div class="cs-summary-meta">
          <span>{{ loading ? '正在同步最新数据' : `更新于 ${loadedText || '刚刚'}` }}</span>
          <button class="cs-refresh" :disabled="loading" title="刷新账本" aria-label="刷新账本" @click="load">
            <svg :class="{ spin: loading }" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 11a8.1 8.1 0 0 0-15.5-2M4 4v5h5M4 13a8.1 8.1 0 0 0 15.5 2M20 20v-5h-5"/></svg>
          </button>
        </div>
      </div>
    </section>

    <section class="card cs-filter-card" :class="{ 'is-filter-open': filterOpen }" aria-label="筛选往来">
      <div class="cs-filter-head">
        <button
          type="button"
          class="cs-filter-toggle"
          aria-controls="cs-filter-controls"
          :aria-expanded="filterOpen"
          :title="filterOpen ? '收起筛选往来' : '展开筛选往来'"
          @click="filterOpen = !filterOpen"
        >
          <span class="cs-filter-label">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 6h16M7 12h10M10 18h4"/></svg>
            <b>筛选往来</b>
            <span class="cs-filter-summary">{{ filterSummary }}</span>
            <i v-if="activeFilterCount" class="cs-filter-badge">{{ activeFilterCount }}</i>
          </span>
          <svg class="cs-filter-chevron" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>
        </button>
        <button v-if="activeFilterCount" class="cs-reset" @click="resetFilters">恢复本月</button>
      </div>
      <!-- 搜索框常驻：筛选条件收起时，仍能直接搜寄卖人 / 委托人。 -->
      <div class="cs-filter-search-row">
        <div class="search-wrap cs-search cs-owner-search" :class="{ 'is-open': searchOpen }">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input
            class="input"
            v-model="q"
            :aria-label="isBuy ? '搜索委托人' : '搜索寄卖人'"
            :placeholder="isBuy ? '搜委托人，支持相似匹配' : '搜寄卖人，支持相似匹配'"
            role="combobox"
            aria-autocomplete="list"
            aria-controls="cs-owner-options"
            :aria-expanded="searchOpen"
            :aria-activedescendant="searchOpen && ownerChoices.length ? `cs-owner-option-${searchIndex}` : undefined"
            @focus="onSearchFocus"
            @input="onSearchInput"
            @blur="closeSearchSoon"
            @keydown="onSearchKeydown"
          />
          <button v-if="q" class="cs-search-clear" aria-label="清空搜索" @click="clearSearch">×</button>
          <div v-if="searchOpen" id="cs-owner-options" class="cs-owner-suggest" role="listbox" :aria-label="isBuy ? '委托人候选' : '寄卖人候选'">
            <div class="cs-owner-suggest-head">
              <b>{{ q.trim() ? '相似匹配' : '快捷选择' }}</b>
              <span>{{ q.trim() ? ownerChoices.length + ' 个候选' : '点选即筛选' }}</span>
            </div>
            <button
              v-for="(choice, index) in ownerChoices"
              :id="`cs-owner-option-${index}`"
              :key="choice.owner"
              type="button"
              class="cs-owner-option"
              :class="{ on: index === searchIndex }"
              role="option"
              :aria-selected="index === searchIndex"
              @mouseenter="searchIndex = index"
              @pointerdown.prevent="selectSearchOwner(choice.owner)"
            >
              <span class="cs-option-avatar" aria-hidden="true">{{ initial(choice.owner) }}</span>
              <span class="cs-option-copy">
                <b>{{ choice.owner }}</b>
                <small v-if="choice.inRange">{{ choice.orders }} 单 · 待{{ isBuy ? '收' : '结' }} {{ fmtMoney(choice.pending) }}</small>
                <small v-else>其他日期有单据</small>
              </span>
              <span v-if="choice.inRange && isPending(choice.pending)" class="cs-option-dot" aria-hidden="true"></span>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M9 18l6-6-6-6"/></svg>
            </button>
            <div v-if="!ownerChoices.length" class="cs-owner-suggest-empty">
              没有找到相似归属人，换个字试试。
            </div>
            <div v-if="ownerChoices.length" class="cs-owner-suggest-foot">
              <span>↑↓ 选择</span>
              <span>Enter 确定</span>
            </div>
          </div>
        </div>
      </div>
      <div id="cs-filter-controls" class="cs-filter-body">
        <div class="cs-filter-body-in">
          <div class="cs-filter-controls">
        <div class="chips cs-range-chips" aria-label="快捷日期">
          <button class="chip" :class="{ on: rangeKey === 'month' }" @click="applyRange('month')">本月</button>
          <button class="chip" :class="{ on: rangeKey === 'lastMonth' }" @click="applyRange('lastMonth')">上月</button>
          <button class="chip" :class="{ on: rangeKey === 'quarter' }" @click="applyRange('quarter')">本季度</button>
          <button class="chip" :class="{ on: rangeKey === 'year' }" @click="applyRange('year')">今年</button>
          <button class="chip" :class="{ on: rangeKey === 'all' }" @click="applyRange('all')">全部</button>
        </div>
        <div class="cs-date-range">
          <input class="input date-input" type="date" v-model="from" aria-label="开始日期" />
          <span class="cs-date-sep" aria-hidden="true">至</span>
          <input class="input date-input" type="date" v-model="to" aria-label="结束日期" />
        </div>
        </div>
      </div>
      </div>
      <button v-if="active" class="cs-active-filter" @click="active = ''">
        只看「{{ active }}」 <span>×</span>
      </button>
    </section>

    <section class="cs-list-section">
      <div class="cs-list-head">
        <div>
          <h3>{{ isBuy ? '委托人往来' : '寄卖人往来' }}</h3>
          <p>{{ isBuy ? '先看待收，再展开核对每一笔垫付。' : '先看待结，再展开核对每一笔销售。' }}</p>
        </div>
        <span class="cs-list-count">{{ list.length }} 人</span>
      </div>

      <div v-if="loading && !data" class="cs-loading" aria-live="polite">
        <div class="skeleton cs-skeleton-hero"></div>
        <div class="cs-skeleton-grid">
          <div v-for="n in 4" :key="n" class="card cs-skeleton-card">
            <span class="skeleton cs-skeleton-avatar"></span>
            <span class="skeleton cs-skeleton-line"></span>
            <span class="skeleton cs-skeleton-line short"></span>
          </div>
        </div>
      </div>

      <div v-else-if="shown.length" class="cs-owner-grid">
        <article
          v-for="owner in shown"
          :key="owner.owner"
          class="card cs-owner-card"
          :class="{ open: active === owner.owner, settled: !isPending(owner.pending) }"
        >
          <div class="cs-owner-top">
            <button class="cs-owner-toggle" :aria-expanded="active === owner.owner" @click="pickOwner(owner.owner)">
              <span class="cs-avatar" aria-hidden="true">{{ initial(owner.owner) }}</span>
              <span class="cs-owner-identity">
                <span class="cs-owner-name">
                  <b>{{ owner.owner }}</b>
                  <span class="cs-status" :class="{ clear: !isPending(owner.pending) }">{{ statusText(owner) }}</span>
                </span>
                <small>{{ owner.orders }} 单 · {{ isBuy ? '区间垫付' : '区间销售' }} {{ fmtMoney(isBuy ? owner.amount : owner.sales) }}</small>
              </span>
              <svg class="cs-chev" :class="{ up: active === owner.owner }" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M6 9l6 6 6-6"/></svg>
            </button>
            <div class="cs-owner-actions">
              <button v-if="owner.owner === '未指定归属'" class="btn btn-sm btn-quiet" @click="openAssign(owner)">补归属</button>
              <button class="btn btn-sm btn-primary" :disabled="!isPending(owner.pending)" @click="openSettle(owner)">
                {{ isBuy ? '收款' : '结算' }}
              </button>
              <button class="btn btn-sm btn-quiet" @click="openHistory(owner)">记录</button>
            </div>
          </div>

          <div class="cs-owner-metrics">
            <div>
              <span>{{ isBuy ? '区间垫付' : '区间销售' }}</span>
              <b class="num">{{ fmtMoney(isBuy ? owner.amount : owner.sales) }}</b>
            </div>
            <div>
              <span>{{ isBuy ? '累计已收' : '累计已结' }}</span>
              <b class="num">{{ fmtMoney(owner.settled) }}</b>
            </div>
            <div class="pending">
              <span>{{ isBuy ? '待收回' : '待结清' }}</span>
              <b class="num">{{ fmtMoney(owner.pending) }}</b>
            </div>
          </div>

          <div class="cs-progress">
            <div class="cs-progress-copy">
              <span>结算进度</span>
              <b>{{ Math.round(settleProgress(owner)) }}%</b>
            </div>
            <div class="cs-progress-track"><i :style="{ width: settleProgress(owner) + '%' }"></i></div>
          </div>

          <div class="cs-owner-preview">
            <span class="cs-preview-label">最近商品</span>
            <div class="cs-preview-list">
              <button
                v-for="item in previewItems(owner)"
                :key="item.id"
                type="button"
                class="cs-preview-item"
                :disabled="itemLoadingId === item.id"
                :title="`查看「${item.product}」的完整信息`"
                @click="openItemDetail(item)"
              >
                <span>{{ item.product }}</span>
                <small v-if="itemLoadingId === item.id">读取中</small>
                <svg v-else width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M9 18l6-6-6-6"/></svg>
              </button>
              <span v-if="!previewItems(owner).length" class="cs-preview-empty">暂无商品明细</span>
              <span v-else-if="owner.items.length > 2" class="cs-preview-more">等 {{ owner.items.length }} 件</span>
            </div>
          </div>

          <div v-if="active === owner.owner" class="cs-owner-detail">
            <div class="cs-detail-head">
              <b>区间往来明细</b>
              <span>{{ owner.items.length }} 笔</span>
            </div>
            <div class="cs-item-row" v-for="item in owner.items" :key="item.id">
              <div class="cs-item-main">
                <button type="button" class="cs-item-link" :title="item.product" :disabled="itemLoadingId === item.id" @click="openItemDetail(item)">
                  {{ item.product }}
                  <span v-if="itemLoadingId === item.id">读取中…</span>
                </button>
                <span>{{ dayOf(isBuy ? item.buy_time : item.sell_time) }} · {{ item.counterparty || '未填写对方' }}</span>
              </div>
              <div class="cs-item-amount">
                <span>{{ isBuy ? '垫付' : '成交价' }}</span>
                <b class="num">{{ fmtMoney(isBuy ? item.buy_price : item.sell_price) }}</b>
              </div>
              <div class="cs-item-amount">
                <span>{{ isBuy ? '运费' : '到手' }}</span>
                <b class="num">{{ fmtMoney(isBuy ? item.buy_shipping : item.sell_net) }}</b>
              </div>
              <div class="cs-item-amount">
                <span>{{ isBuy ? '合计垫付' : '费用' }}</span>
                <b class="num">{{ fmtMoney(isBuy ? item.buy_price + item.buy_shipping : item.sell_fee + item.sell_shipping) }}</b>
              </div>
            </div>
          </div>
        </article>
      </div>

      <div v-else-if="!err" class="empty cs-empty">
        <div class="empty-badge">
          <svg v-if="isBuy" width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M6 2 3 6v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6l-3-4z"/><path d="M3 6h18"/><path d="M16 10a4 4 0 0 1-8 0"/></svg>
          <svg v-else width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.9"/><path d="M16 3.1a4 4 0 0 1 0 7.8"/></svg>
        </div>
        <div class="empty-title">
          {{ rawList.length
            ? '没有匹配的往来人'
            : rangeKey !== 'all' && historyOrders
              ? `${rangeText}没有${isBuy ? '代采' : '寄卖'}单`
              : (isBuy ? '这个区间没有代采单' : '这个区间没有寄卖单') }}
        </div>
        <div class="empty-sub">
          <template v-if="rawList.length">换个名字，或清除当前筛选后再看。</template>
          <template v-else-if="rangeKey !== 'all' && historyOrders">
            全部历史还有 {{ fmtNum(historyOrders) }} 单{{ historyLatest ? `，最近一单在 ${historyLatest.slice(0, 10)}` : '' }}。日期筛选没有改坏数据，切到全部时间即可查看。
          </template>
          <template v-else-if="isBuy">录入买单时把「货物来源」选为「代采」并填写委托人，垫付款会按人汇总到这里。</template>
          <template v-else>录入或编辑卖单时把「货物来源」选为「寄卖」并填写寄卖人，销售款会按人汇总到这里。</template>
        </div>
        <div class="cs-empty-actions">
          <button v-if="rawList.length || q" class="btn btn-sm btn-quiet" @click="resetFilters">清除筛选</button>
          <button v-else-if="rangeKey !== 'all'" class="btn btn-sm btn-primary" @click="applyRange('all')">查看全部历史</button>
          <button v-else class="btn btn-sm btn-quiet" @click="resetFilters">回到本月</button>
        </div>
      </div>
    </section>

    <RecordModal v-if="itemDetail" :record="itemDetail" view-only @close="closeItemDetail" @saved="onItemSaved" />

    <div v-if="assignOpen" class="modal-mask set-sheet" @pointerdown="assignMask.onPointerDown" @click="assignMask.onClick">
      <div ref="assignDialog" class="modal cs-assign-modal" role="dialog" aria-modal="true" tabindex="-1" aria-labelledby="cs-assign-title">
        <div class="modal-head cs-modal-head">
          <div class="cs-modal-person">
            <span class="cs-avatar" aria-hidden="true">?</span>
            <div>
              <span>{{ isBuy ? '补录代采委托人' : '补录寄卖人' }}</span>
              <h3 id="cs-assign-title">未指定归属</h3>
            </div>
          </div>
          <button class="x" @click="closeAssign" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <p class="cs-assign-copy">
            这里有 {{ assignTarget?.items?.length || 0 }} 笔{{ isBuy ? '代采垫付' : '寄卖销售' }}还没有归属。补录后会一起移到该归属人名下，金额和单据内容不变。
          </p>
          <div class="field">
            <label for="cs-assign-owner">归属人</label>
            <input
              id="cs-assign-owner"
              v-model="assignName"
              class="input"
              list="cs-assign-owner-names"
              maxlength="100"
              autocomplete="off"
              placeholder="输入姓名，或选择已有归属人"
              @keydown.enter.prevent="submitAssign"
            />
            <datalist id="cs-assign-owner-names">
              <option v-for="name in assignNames" :key="name" :value="name" />
            </datalist>
          </div>
          <div v-if="assignErr" class="alert-err" role="alert">{{ assignErr }}</div>
        </div>
        <div class="modal-foot">
          <button class="btn btn-ghost" @click="closeAssign">取消</button>
          <button class="btn btn-primary cs-confirm-btn" :disabled="assignBusy || !assignName.trim()" @click="submitAssign">
            <span v-if="assignBusy" class="spin"></span>
            {{ assignBusy ? '正在补录…' : '保存归属' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="settleOpen" class="modal-mask set-sheet" @pointerdown="settleMask.onPointerDown" @click="settleMask.onClick">
      <div ref="settleDialog" class="modal cs-settle-modal" role="dialog" aria-modal="true" tabindex="-1" :aria-label="isBuy ? '代采收款' : '寄卖结算'">
        <div class="modal-head cs-modal-head">
          <div class="cs-modal-person">
            <span class="cs-avatar" aria-hidden="true">{{ initial(settleOwner?.owner) }}</span>
            <div>
              <span>{{ isBuy ? '代采收款' : '寄卖结算' }}</span>
              <h3>{{ settleOwner?.owner }}</h3>
            </div>
          </div>
          <button class="x" @click="closeSettle" aria-label="关闭">×</button>
        </div>
        <div class="modal-body">
          <div class="cs-settle-balance">
            <span>当前{{ isBuy ? '待收' : '待结' }}</span>
            <b class="num">{{ fmtMoney(settlePending) }}</b>
            <small>{{ isBuy ? '扣除历史已收款后的余额' : '扣除历史已结算后的余额' }}</small>
          </div>
          <div class="field">
            <label for="cs-settle-amount">{{ isBuy ? '本次收到金额' : '本次结算金额' }}</label>
            <div class="cs-amount-input">
              <span>¥</span>
              <input id="cs-settle-amount" class="input num" type="number" min="0.01" step="0.01" v-model.number="settleAmount" />
            </div>
            <div class="cs-quick-amounts">
              <button :class="{ on: round2(settleAmount) === round2(settlePending) }" @click="setSettleAmount('all')">全部</button>
              <button :class="{ on: round2(settleAmount) === round2(settlePending / 2) }" @click="setSettleAmount(0.5)">一半</button>
              <button @click="settleAmount = 0">重新填写</button>
            </div>
          </div>
          <div class="cs-settle-remaining" :class="{ over: Number(settleAmount) > settlePending }">
            <span v-if="Number(settleAmount) > settlePending">本次金额超过当前待{{ isBuy ? '收' : '结' }}，保存后会形成多收 / 多结。</span>
            <span v-else>完成后剩余 {{ fmtMoney(settleRemaining) }}</span>
            <b>{{ round2(settleAmount) > 0 ? fmtMoney(round2(settleAmount)) : '—' }}</b>
          </div>
          <div class="field">
            <label for="cs-settle-note">备注 <small>可选</small></label>
            <input id="cs-settle-note" class="input" v-model="settleNote" placeholder="如：微信转账 / 现金" />
          </div>
          <p class="cs-settle-tip">
            {{ isBuy ? '默认填入当前待收金额，也可以按实际收到的金额修改。' : '默认填入当前待结金额，也可以按实际支付金额修改。' }}
          </p>
        </div>
        <div class="modal-foot">
          <button class="btn btn-ghost" @click="closeSettle">取消</button>
          <button class="btn btn-primary cs-confirm-btn" :disabled="settleBusy || Number(settleAmount) <= 0" @click="submitSettle">
            <span v-if="settleBusy" class="spin"></span>
            {{ isBuy ? '确认收款' : '确认结算' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="historyOpen" class="modal-mask set-sheet" @pointerdown="historyMask.onPointerDown" @click="historyMask.onClick">
      <div ref="historyDialog" class="modal cs-history-modal" role="dialog" aria-modal="true" tabindex="-1" aria-label="结算记录">
        <div class="modal-head cs-modal-head">
          <div class="cs-modal-person">
            <span class="cs-avatar" aria-hidden="true">{{ initial(historyOwner) }}</span>
            <div>
              <span>{{ historyKind === 'buy' ? '代采收款记录' : '寄卖结算记录' }}</span>
              <h3>{{ historyOwner }}</h3>
            </div>
          </div>
          <button class="x" @click="closeHistory" aria-label="关闭">×</button>
        </div>
        <div class="modal-body cs-history-body">
          <div v-if="historyLoading" class="cs-history-state" aria-live="polite">
            <span class="spin"></span>
            正在读取记录…
          </div>
          <div v-else-if="historyErr" class="cs-history-state error">
            <span>{{ historyErr }}</span>
            <button class="btn btn-sm btn-quiet" @click="fetchHistory">重试</button>
          </div>
          <template v-else>
            <div v-if="history.length" class="cs-history-summary">
              <div>
                <span>共 {{ history.length }} 笔</span>
                <small>{{ historyKind === 'buy' ? '累计已收' : '累计已结' }}</small>
              </div>
              <b class="num">{{ fmtMoney(historyTotal) }}</b>
            </div>
            <div v-if="history.length" class="cs-history-list">
              <article v-for="row in history" :key="row.id" class="cs-history-row">
                <span class="cs-history-dot" aria-hidden="true"></span>
                <div class="cs-history-main">
                  <b class="num">{{ fmtMoney(row.amount) }}</b>
                  <span>{{ String(row.settled_at || '').slice(0, 16) || '—' }}</span>
                  <small>{{ row.note || '未填写备注' }}</small>
                </div>
                <button class="cs-history-delete" @click="delSettlement(row)">删除</button>
              </article>
            </div>
            <div v-else class="cs-history-empty">
              <div class="empty-badge">
                <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M4 5h16M6 5v15h12V5M9 9h6M9 13h6"/></svg>
              </div>
              <b>还没有{{ historyKind === 'buy' ? '收款' : '结算' }}记录</b>
              <span>完成第一笔{{ historyKind === 'buy' ? '收款' : '结算' }}后，会在这里留下流水。</span>
            </div>
          </template>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="closeHistory">完成</button></div>
      </div>
    </div>
  </div>
</template>
