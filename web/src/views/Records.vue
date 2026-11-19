<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { api, downloadFile } from '../api'
import { currentShopId } from '../shopId'
import ImageViewer from '../components/ImageViewer.vue'
import AnimatedNumber from '../components/AnimatedNumber.vue'
import GroupPicker from '../components/GroupPicker.vue'
import { confirm } from '../confirm'
import { undoAction } from '../feedback'
import { fmtMoney, fmtNum, shortTime, todayStr } from '../utils'

const props = defineProps({ refreshKey: { type: Number, default: 0 }, highlightId: { type: Number, default: 0 }, focusQuery: { type: String, default: '' }, focusNorm: { type: String, default: '' }, focusType: { type: String, default: '' }, focusNonce: { type: Number, default: 0 } })
const emit = defineEmits(['add', 'edit', 'refresh', 'notify', 'goto-group', 'batch'])

const q = ref('')
const type = ref('')
// 底部导航「明细」红点的来源：这些卖单还没挂成本，利润只能按 0 算
const todoUnlinked = ref(0)
async function loadTodo() {
  try { todoUnlinked.value = Number((await api.todoCount()).unlinked) || 0 } catch (e) { todoUnlinked.value = 0 }
}
const from = ref('')
const to = ref('')
let savedSort = {}
try { savedSort = JSON.parse(localStorage.getItem('xy_sort') || '{}') } catch (e) {}
const sort = ref(savedSort.sort || 'time')
const order = ref(savedSort.order || 'desc')
// 价格区间：只留最低价 / 最高价（原来还有「精确金额」，太绕，去掉了）
const minAmount = ref('')
const maxAmount = ref('')
const batchField = ref('note')
const batchValue = ref('')
const colPanelOpen = ref(false)
let savedCols = { shot: false, shipping: true, fee: true, time: true }
try { savedCols = { ...savedCols, ...JSON.parse(localStorage.getItem('xy_columns') || '{}') } } catch (e) {}
const showCols = ref(savedCols)
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
// 手机端「数据汇总」默认折叠：首页已有同样的指标，明细页最重要的是尽快看到记录列表。
// 桌面端由 CSS 强制展开，这个开关只作用于窄屏。
const sumOpen = ref(false)
const items = ref([])
const sum = ref(null)
const loading = ref(false)
const error = ref('')
const selected = ref([])
// 选择模式：手机端默认不显示勾选框（太占版面也不好看），长按任意卡片才进入，
// 进入后所有卡片一起露出方框，可以连续多选。取消选择到 0 条时自动退出。
const selectMode = ref(false)
const expanded = ref(null)
// 全屏看图：记录当前查看的图片地址（原图，viewer 里可自由缩放）
const viewerSrc = ref('')
function openImage(r) { if (r.attachment) viewerSrc.value = api.attachmentUrl(r.id, { file: r.attachment }) }
// 缩略图地址：列表里只拉 160px，详情里拉 720px，都带内容指纹可强缓存
function thumbUrl(r, w) { return api.attachmentUrl(r.id, { w, file: r.attachment }) }
// 记录 -> 所属联单 映射（一次取回全部联单建立索引，点击即联动）
const groupMap = ref({})

// 明细分类：全部 / 买单 / 卖单 / 已结算（与后端 type 过滤一致）
// 「已结算」= 整张联单完结的 + 卖单自己填好成本、自动结算的
const typeOptions = [
  { v: '', t: '全部' }, { v: 'buy', t: '买单' },
  { v: 'sell', t: '卖单' }, { v: 'unlinked', t: '未挂成本' }, { v: 'settled', t: '已结算' },
  { v: 'discarded', t: '弃单' }
]
const sortOptions = [
  { v: 'time', t: '交易时间' }, { v: 'amount', t: '金额' },
  { v: 'created', t: '录入时间' }
]
const rangePresets = [
  { t: '全部', days: 0 }, { t: '近30天', days: 30 },
  { t: '近90天', days: 90 }, { t: '今年', days: -1 }
]
const activeRange = ref(0)
// 移动端筛选默认收起，避免日期/金额/排序/导出一次性占掉半屏
const filterOpen = ref(false)

let settingRange = false
function applyRange(days, idx) {
  settingRange = true
  activeRange.value = idx
  if (days === 0) { from.value = ''; to.value = '' }
  else if (days === -1) { from.value = new Date().getFullYear() + '-01-01'; to.value = todayStr() }
  else {
    const d = new Date(); d.setDate(d.getDate() - days)
    const p = x => String(x).padStart(2, '0')
    from.value = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
    to.value = todayStr()
  }
  reload(true)
  setTimeout(() => { settingRange = false }, 0)
}
let timer = null
let loadSeq = 0
function params(extra = {}) {
  const p = { page: page.value, page_size: pageSize.value, sort: sort.value, order: order.value }
  // 商品跳转优先用 pn（product_norm 精确命中），此时 q 只是搜索框回显，不能参与过滤
  if (productFilter.value && productFilter.value.norm) p.pn = productFilter.value.norm
  else if (q.value.trim()) p.q = q.value.trim()
  if (type.value) p.type = type.value
  if (focusId.value) p.focus = focusId.value
  if (from.value) p.from = from.value
  if (to.value) p.to = to.value
  if (minAmount.value !== '') p.min_amount = minAmount.value
  if (maxAmount.value !== '') p.max_amount = maxAmount.value
  return { ...p, ...extra }
}
async function load() {
  // 请求序号：实时同步与手动刷新可能并发，先发的后到会把新数据覆盖成旧的。
  // 只认最后一次发起的请求，过期的响应直接丢弃。
  const seq = ++loadSeq
  loading.value = true; error.value = ''
  try {
    const [l, s, gs] = await Promise.all([
      api.list(params()),
      api.stats(params({ page: 1, page_size: 1 })),
      api.groupsBrief().catch(() => ({ items: [] }))
    ])
    if (seq !== loadSeq) return
    items.value = l.items; total.value = l.total; sum.value = s
    // 数据变少后旧页码可能落在空页：切换店铺、批量删除、筛选收窄都会触发。
    // 不回退的话用户会看到「暂无记录」，但实际有数据，只是当前页超界了。
    if (!l.items.length && l.total > 0 && page.value > 1) {
      page.value = 1
      pagingToTop = false // 回退到第一页属于重置，交给正常的列表顶部逻辑
      loading.value = false
      return load()
    }
    selected.value = selected.value.filter(id => l.items.some(i => i.id === id))
    const gm = {}
    for (const g of (gs.items || [])) {
      for (const id of (g.item_ids || [])) {
        (gm[id] = gm[id] || []).push({ id: g.id, title: g.title, settled: !!g.settled_at })
      }
    }
    groupMap.value = gm
  } catch (e) {
    if (seq === loadSeq) error.value = e.message || '加载失败'
  }
  finally {
    if (seq === loadSeq) {
      loading.value = false
      // 列表渲染完再定位新保存的那条，否则元素还不存在
      nextTick(() => {
        tryFlash()
        // 翻页后回到列表顶部。必须等列表 DOM 更新完再滚，否则高度还没撑起来会被夹回去。
        // 有高亮定位时优先服从高亮（tryFlash 会 scrollIntoView），避免两处滚动互相打架。
        if (pagingToTop && !pendingFlash) {
          pagingToTop = false
          scrollToListTop()
        } else {
          pagingToTop = false
        }
      })
    }
  }
}

// ---------- 刚保存的那条：滚到眼前 + 闪一下 ----------
// 保存成功只弹一句「已保存」是不够的：24 条记录里根本认不出哪条是刚加的。
// 删除先淡出再刷新：直接消失会让人怀疑「到底删掉了没有」
const leaving = ref([])
let leaveTimer = 0
function markLeaving(ids) {
  leaving.value = ids.slice()
  clearTimeout(leaveTimer)
  return new Promise((resolve) => { leaveTimer = setTimeout(() => { leaving.value = []; resolve() }, 200) })
}
const flashId = ref(0)
const focusId = ref(0)   // 非 0 = 正在定位某一条（列表只显示它）
// 批量挂成本
const costOpen = ref(false)
const costQ = ref('')
const costBusy = ref(false)
const costFlash = ref(false)   // 挂成本成功后列表闪一下，给出"改了"的反馈
const costList = ref([])
const unitOf = (p) => {
  const q = Number(p.quantity) > 0 ? Number(p.quantity) : 1
  return ((Number(p.buy_price) || 0) + (Number(p.buy_shipping) || 0)) / q
}
const costCandidates = computed(() => {
  const k = costQ.value.trim().toLowerCase()
  if (!k) return costList.value.slice(0, 30)
  return costList.value.filter((p) => ((p.product || '') + (p.buy_order_no || '') + (p.buy_counterparty || '')).toLowerCase().includes(k)).slice(0, 30)
})
async function openCostPicker() {
  if (!selected.value.length) { emit('notify', '先勾选要挂成本的卖单', true); return }
  costOpen.value = true
  costQ.value = ''
  try { costList.value = (await api.purchases()).items || [] } catch (e) { costList.value = [] }
}
async function linkTo(p) {
  if (costBusy.value) return
  costBusy.value = true
  try {
    const r = await api.linkPurchases({ sale_ids: selected.value.slice(), purchase_id: p.id })
    costOpen.value = false
    selected.value = []
    costFlash.value = true
    setTimeout(() => { costFlash.value = false }, 600)
    emit('notify', `已给 ${r.linked || 0} 张卖单挂上成本` + (r.group_id ? '，并并入联单' : ''))
    emit('refresh')
    reload(true)
  } catch (e) {
    emit('notify', e.message || '挂成本失败', true)
  } finally {
    costBusy.value = false
  }
}
// ---------- 批量标记「无成本」：代卖 / 寄卖 / 赠送这类本来就没有货品成本的卖单 ----------
const costFreeBusy = ref(false)
const selectedRows = computed(() => items.value.filter((r) => selected.value.includes(r.id)))
// 只有独立卖单能标：挂了买进单的成本来源已经明确；联单内卖单由后端按联单核算再兜一道
const canMarkFree = computed(() => selectedRows.value.length > 0 && selectedRows.value.every((r) => r.has_sell && !r.parent_id))
const allCostFree = computed(() => selectedRows.value.length > 0 && selectedRows.value.every((r) => r.cost_free))
// 落库 + 反馈，批量条和「挂成本」选单弹窗共用
async function applyCostFree(rows, free) {
  costFreeBusy.value = true
  try {
    const r = await api.markCostFree(rows.map((x) => x.id), free)
    const tail = r.skipped ? `，${r.skipped} 单不适用已跳过` : ''
    emit('notify', (free ? `已标记 ${r.updated} 单为无成本` : `已取消 ${r.updated} 单的无成本标记`) + tail)
    selected.value = []
    emit('refresh')
    reload(true)
  } catch (e) {
    emit('notify', e.message || '标记失败', true)
  } finally {
    costFreeBusy.value = false
  }
}
const FREE_MSG = '代卖、寄卖、赠送这类本来就没有货品成本的单，标记后不再提示「未挂成本」，利润按销售额全额计（只扣运费和服务费）。随时可以取消。'
async function toggleCostFree() {
  const rows = selectedRows.value
  if (!rows.length) { emit('notify', '先勾选要标记的卖单', true); return }
  if (!canMarkFree.value) { emit('notify', '只有没挂买进单的独立卖单才能标无成本', true); return }
  const free = !allCostFree.value
  const ok = await confirm(free ? {
    title: `标记 ${rows.length} 单为无成本？`,
    message: FREE_MSG,
    confirmText: '标记无成本'
  } : {
    title: `取消 ${rows.length} 单的无成本标记？`,
    message: '取消后会重新出现在「未挂成本」里，需要给它们挂买进单或手动填成本。',
    confirmText: '取消标记'
  })
  if (!ok) return
  await applyCostFree(rows, free)
}
// 「挂成本」选单弹窗里的出口：这些单根本没有成本，不用挑买进单。
// 只处理还没标的独立卖单；已挂买进单/已在含买进单联单里的会被后端跳过。
async function markNoCostFromPicker() {
  costOpen.value = false
  const rows = selectedRows.value.filter((r) => r.has_sell && !r.parent_id && !r.cost_free)
  if (!rows.length) { emit('notify', '选中的单都已有成本或已标无成本', true); return }
  const ok = await confirm({
    title: `把 ${rows.length} 单标为无成本？`,
    message: FREE_MSG,
    confirmText: '标记无成本'
  })
  if (!ok) return
  await applyCostFree(rows, true)
}
// ---------- 智能挂成本：按商品名给未挂成本的卖单找买进单，用户确认后再挂 ----------
const autoOpen = ref(false)
const autoLoading = ref(false)
const autoBusy = ref(false)
const autoRows = ref([])
const autoStat = ref({ matched: 0, ambiguous: 0, unmatched: 0, total: 0 })
const autoPicked = computed(() => autoRows.value.filter((r) => r._pick && r._purchase))
// 能找到买进单的排前面；找不到的默认折叠，49 条里 47 条配不上时别让它们把界面刷满
const autoPickable = computed(() => autoRows.value.filter((r) => !!r.suggest))
const autoUnmatched = computed(() => autoRows.value.filter((r) => !r.suggest))
const showUnmatched = ref(false)
async function openAutoLink() {
  autoOpen.value = true
  autoLoading.value = true
  autoRows.value = []
  showUnmatched.value = false
  estimateMode.value = false
  try {
    const r = await api.autoLinkPreview()
    autoRows.value = (r.items || []).map((x) => ({
      ...x,
      // 默认只勾「同名且唯一」的：同名多单、名字相近的都留给用户确认
      _pick: !!x.suggest && x.reason === '',
      _purchase: x.suggest ? x.suggest.purchase_id : 0
    }))
    autoStat.value = { matched: r.matched || 0, ambiguous: r.ambiguous || 0, unmatched: r.unmatched || 0, total: r.total || 0 }
  } catch (e) {
    emit('notify', e.message || '匹配失败', true)
    autoOpen.value = false
  } finally {
    autoLoading.value = false
  }
}
// ---------- 估算模式：没有买进单的卖单，按售价比例直接填成本 ----------
const estimateMode = ref(false)
const estimatePct = ref(70)
const estimateRows = ref([])
const estimatePicked = computed(() => estimateRows.value.filter((r) => r._pick && Number(r._cost) > 0))
function enterEstimate() {
  estimateMode.value = true
  estimateRows.value = autoUnmatched.value.map((r) => ({
    sale_id: r.sale_id,
    product: r.product,
    sale_price: r.sale_price,
    _cost: Math.round((Number(r.sale_price) || 0) * (Number(estimatePct.value) || 0)) / 100,
    _pick: true
  }))
}
function applyPct() {
  const pct = Number(estimatePct.value) || 0
  for (const r of estimateRows.value) {
    r._cost = Math.round((Number(r.sale_price) || 0) * pct) / 100
  }
}
async function submitEstimate() {
  const items = estimatePicked.value.map((r) => ({ sale_id: r.sale_id, unit_cost: Number(r._cost) }))
  if (!items.length) return
  autoBusy.value = true
  try {
    const r = await api.estimateCost(items)
    const skipped = items.length - (r.updated || 0)
    autoOpen.value = false
    emit('notify', `已给 ${r.updated || 0} 条卖单填上估算成本` + (skipped > 0 ? `，${skipped} 条跳过` : ''))
    emit('refresh')
    reload(true)
  } catch (e) {
    emit('notify', e.message || '填成本失败', true)
  } finally {
    autoBusy.value = false
  }
}
function autoToggleAll(on) {
  for (const r of autoRows.value) {
    if (r.suggest) r._pick = on
  }
}
async function applyAutoLink() {
  const links = autoPicked.value.map((r) => ({ sale_id: r.sale_id, purchase_id: r._purchase }))
  if (!links.length) return
  autoBusy.value = true
  try {
    const r = await api.autoLinkApply(links)
    const skipped = links.length - (r.linked || 0)
    autoOpen.value = false
    emit('notify', `已挂上 ${r.linked || 0} 条成本` + (skipped > 0 ? `，${skipped} 条已跳过` : '') + (r.group_id ? '，并并入联单' : ''))
    emit('refresh')
    reload(true)
  } catch (e) {
    emit('notify', e.message || '挂成本失败', true)
  } finally {
    autoBusy.value = false
  }
}
function clearFocus() { focusId.value = 0; pendingFlash = 0; page.value = 1; load() }
let pendingFlash = 0
let flashTimer = 0
function tryFlash() {
  if (!pendingFlash) return
  const el = document.querySelector(`[data-rid="${pendingFlash}"]`)
  if (!el) return
  const id = pendingFlash
  pendingFlash = 0
  flashId.value = id
  try { el.scrollIntoView({ block: 'center', behavior: 'smooth' }) } catch (e) { /* 老内核不支持平滑滚动 */ }
  clearTimeout(flashTimer)
  flashTimer = setTimeout(() => { if (flashId.value === id) flashId.value = 0 }, 1700)
}
// 商品跳转的过滤状态要提前声明：下面定位单条记录的 watch 也要清它
const productFilter = ref(null)   // { label, norm } 非空 = 当前是被「商品利润排行」点进来的
let settingQ = false              // 区分「跳转时程序写入搜索框」和「用户自己改搜索框」

watch(() => props.highlightId, (id) => {
  if (!id) return
  focusId.value = id        // 只显示这一条，保证不管排第几页都能看到
  pendingFlash = id
  // 定位刚保存的记录时必须清掉商品过滤：新记录往往不属于之前筛选的那个商品，
  // 留着 pn 会让列表直接空白，看起来像「保存完数据没了」。
  productFilter.value = null
  q.value = ''
  page.value = 1
  load()
}, { immediate: true })
function reload(reset = true) { if (reset) page.value = 1; load() }
// 从首页「商品利润排行」点某一行进来：按商品过滤，直接看到这个商品的全部单据。
// 明细页是 v-if + key 重建的，挂载时 focusQuery 已经有值、之后不会再变，
// 所以必须监听每次跳转都会自增的 focusNonce，并 immediate 生效一次。
// 过滤走 product_norm（pn 参数）而不是 q：排行里大小写/空格/标点不同的写法已合并成一行，
// 用原文 LIKE 只能筛出其中一部分单据。
// jumping：跳转时一次性改多个筛选条件，别让下面按条件变化的 watch 再补发一次请求
let jumping = false
watch(() => props.focusNonce, () => {
  const v = (props.focusQuery || '').trim()
  const ft = (props.focusType || '').trim()
  if (!v && !ft) return          // 没带条件（例如首页「查看全部明细」）走默认加载
  jumping = true
  focusId.value = 0
  pendingFlash = 0
  activeRange.value = 0
  from.value = ''
  to.value = ''
  productFilter.value = v ? { label: v, norm: (props.focusNorm || '').trim() } : null
  settingQ = true
  q.value = v
  nextTick(() => { settingQ = false })
  type.value = ft
  page.value = 1
  load()
  nextTick(() => { jumping = false })
  // 外面恢复的是上次离开明细页时的滚动位置，这里要压回列表顶部
  setTimeout(scrollToListTop, 80)
}, { immediate: true })
// 用户自己动了搜索框：退出「只看某商品」，回到普通关键词搜索
watch(q, () => {
  if (settingQ) return
  if (productFilter.value) productFilter.value = null
})
// 翻页：分页按钮在列表最底部，点完后视口还停在页尾，新一页的数据在屏幕外看不到。
// 所以翻页后要主动把视口带回列表顶部，让用户从新一页的第一条开始读。
// 只对翻页生效，筛选/排序走 reload(true) 仍回第一页顶部。
const recordsCardRef = ref(null)
let pagingToTop = false
function turnPage(delta) {
  const next = page.value + delta
  if (next < 1 || next > totalPages()) return
  pagingToTop = true
  page.value = next
  load()
}
// 滚到列表卡片顶部（不是整个文档顶部）：上面还有页面标题和筛选栏，
// 滚到文档 0 反而会让用户再往下找一次数据。减去 sticky 顶栏高度避免被遮住。
function scrollToListTop() {
  const el = recordsCardRef.value
  // 目标位置只取决于列表卡片上方的元素高度（标题+筛选栏，固定不变），
  // 与这一页有多少行无关，所以算一次就够，不需要轮询校正。
  let y = 0
  if (el) y = el.getBoundingClientRect().top + window.scrollY - 84
  y = Math.max(y, 0)
  try { window.scrollTo({ top: y, behavior: 'smooth' }) } catch (e) { window.scrollTo(0, y) }
}
function onSearchInput() { clearTimeout(timer); timer = setTimeout(() => reload(true), 300) }
// 卡片上的「同商品」：按这个商品的归一化名精确筛出全部单据。
// 走 productFilter（pn 参数）而不是搜索框 LIKE——同名不同写法（空格/大小写/标点）也能一起筛出来。
function filterByProduct(r) {
  const label = (r && r.product ? r.product : '').trim()
  if (!label) return
  const norm = (r.product_norm || r.norm || '').trim()
  activeRange.value = 0
  from.value = ''
  to.value = ''
  focusId.value = 0
  settingQ = true
  q.value = label
  productFilter.value = { label, norm }
  nextTick(() => { settingQ = false })
  page.value = 1
  load()
  setTimeout(scrollToListTop, 80)
}
// 清空搜索必须同步把 productFilter 也清掉：
// watch(q) 是下一个 tick 才跑，而 reload() 立刻就会读 params()，
// 只清 q 的话请求里还带着 pn（商品跳转的精确过滤），列表看起来「点了没反应」。
function clearSearch() {
  q.value = ''
  productFilter.value = null
  reload(true)
}
function clearFilters() {
  q.value = ''; type.value = ''; from.value = ''; to.value = ''
  minAmount.value = ''; maxAmount.value = ''
  sort.value = 'time'; order.value = 'desc'; activeRange.value = 0
  productFilter.value = null
  focusId.value = 0
  reload(true)
}
// 换排序时列表整块换掉，没有任何位移暗示，很容易以为「点了没反应」。
// 短暂降透明度再恢复，给一次「已经重排」的确认。
const resorting = ref(false)
let resortTimer = 0
watch([sort, order], () => {
  clearTimeout(resortTimer)
  resorting.value = false
  nextTick(() => {
    resorting.value = true
    resortTimer = setTimeout(() => { resorting.value = false }, 240)
  })
})

const totalPages = () => Math.max(1, Math.ceil(total.value / pageSize.value))
const allChecked = computed(() => items.value.length > 0 && items.value.every(i => selected.value.includes(i.id)))
const hasFilters = computed(() => !!(q.value.trim() || type.value || from.value || to.value || minAmount.value || maxAmount.value))
// 展开行 colspan 需随「自定义列」隐藏情况变化，固定 8 会错位
const visibleColCount = computed(() => 5 + (showCols.value.shot ? 1 : 0) + (showCols.value.shipping ? 1 : 0) + (showCols.value.fee ? 1 : 0) + (showCols.value.time ? 1 : 0))
const activeFilterCount = computed(() => [q.value.trim(), type.value, from.value, to.value, minAmount.value, maxAmount.value].filter(v => v !== '' && v != null).length)
const filteredProfit = computed(() => Number(sum.value?.total_profit) || 0)
function toggleAll() {
  if (allChecked.value) selected.value = []
  else selected.value = items.value.map(i => i.id)
}
// 选中清空就退出选择模式（批量删除、切换筛选等都会清空）
watch(() => selected.value.length, (n) => { if (!n) selectMode.value = false })

function toggleOne(id) {
  const i = selected.value.indexOf(id)
  if (i >= 0) selected.value.splice(i, 1); else selected.value.push(id)
  // 一条都不剩就退出选择模式，避免勾选框一直挂在卡片上
  if (!selected.value.length) {
    selectMode.value = false
    emit('notify', '已退出多选', false, null, { tone: 'info' })
  }
}

async function del(r) {
  if (blockIfSettled(r, '删除')) return
  const ok = await confirm({
    title: '删除这条记录？',
    message: `「${r.product || '未命名商品'}」将从明细中移除。`,
    detail: [`订单号 ${r.sell_order_no || r.buy_order_no || '—'}`, '此操作不可恢复。'],
    confirmText: '删除', danger: true
  })
  if (!ok) return
  try {
    const res = await api.remove(r.id)
    // 删掉的正好是「定位」的那条：清掉定位，否则列表会变成 0 条
    if (focusId.value === r.id) focusId.value = 0
    emit('notify', '已删除', false, undoAction(res.undo_token, {
      after: load,
      notify: (t) => emit('notify', t),
      doneText: '已撤销删除'
    }))
    if (items.value.length === 1 && page.value > 1) page.value--
    await markLeaving([r.id])
    load()
  } catch (e) { emit('notify', '删除失败：' + e.message, true) }
}
async function batchDel() {
  if (!selected.value.length) return
  const locked = items.value.filter(i => selected.value.includes(i.id) && groupSettled(i))
  if (locked.length) {
    emit('notify', `选中的记录里有 ${locked.length} 条属于已完结联单，请先在「联单」页重新开启后再删`, true)
    return
  }
  const ok = await confirm({
    title: '批量删除记录？',
    message: `将删除选中的 ${selected.value.length} 条记录。`,
    detail: '此操作不可恢复。',
    confirmText: `删除 ${selected.value.length} 单`, danger: true
  })
  if (!ok) return
  try {
    const res = await api.batchRemove(selected.value)
    const deletedIds = selected.value.slice()
    selected.value = []
    // 定位的那条也在删除列表里时清掉定位，否则列表会空
    if (deletedIds.includes(focusId.value)) focusId.value = 0
    emit('notify', `已删除 ${res.deleted} 单`, false, undoAction(res.undo_token, {
      after: load,
      notify: (t) => emit('notify', t),
      doneText: '已撤销批量删除'
    }))
    if (items.value.length === res.deleted && page.value > 1) page.value--
    await markLeaving(deletedIds)
    load()
  } catch (e) { emit('notify', '批量删除失败：' + e.message, true) }
}
// 导出三个入口共用一套「忙碌 → 完成 / 失败」反馈：
// 以前点完只有浏览器自己的下载条，手机上完全看不出点没点上。
const exporting = ref('')   // '' | 'csv' | 'columns' | 'profit'
async function runExport(which, url, fallbackName, doneText) {
  if (exporting.value) return
  exporting.value = which
  try {
    await downloadFile(url, fallbackName)
    emit('notify', doneText)
  } catch (e) {
    emit('notify', '导出失败：' + (e.message || '网络异常'), true)
  } finally {
    exporting.value = ''
  }
}
async function exportCsv() {
  const p = new URLSearchParams({ format: 'csv', scope: 'filtered' })
  // 直链下载带不了请求头，店铺必须显式放进 query
  const sid = currentShopId()
  if (sid) p.set('shop', String(sid))
  if (q.value.trim()) p.set('q', q.value.trim())
  if (type.value) p.set('type', type.value)
  if (from.value) p.set('from', from.value)
  if (to.value) p.set('to', to.value)
  const n = total.value || items.value.length
  await runExport('csv', '/api/export?' + p.toString(), `闲鱼明细-${todayStr()}.csv`,
    n ? `已导出 ${n} 条记录` : '已导出空表（当前筛选没有记录）')
}
// 列表只显示「对方昵称」：买单是卖家、卖单是买家。
// 订单号又长又难记，扫列表时帮不上忙，放详情里看就够了。
const counterpartyOf = (r) => {
  const name = r.has_sell ? (r.sell_counterparty || '') : (r.buy_counterparty || '')
  return String(name).trim()
}
const childCountOf = (id) => items.value.filter(i => i.parent_id === id).length
const parentProduct = (id) => (items.value.find(i => i.id === id) || {}).product || ''
const groupsOf = (id) => groupMap.value[id] || []
// 「加入联单」：面板里要显示这些记录已经属于哪些联单
const groupPickerIds = ref(null)
function openGroupPicker(ids) {
  const list = (Array.isArray(ids) ? ids : [ids]).filter(Boolean)
  if (!list.length) return
  groupPickerIds.value = list
}
const groupMembership = computed(() => {
  const m = {}
  for (const id of (groupPickerIds.value || [])) {
    m[id] = (groupMap.value[id] || []).map(g => g.title)
  }
  return m
})
// 明细分类标签：已完结联单内的记录优先显示「完结单」，否则按交易类型显示买单 / 卖单
// 「完结章」只在整张联单完结时出现。
// 单条完结（旧数据里的 settled_at）不再出章：现在卖单只要填了单件成本就算核算完成，
// 不再有「单独完结」这个动作。
const groupSettled = (r) => (groupsOf(r.id) || []).some(g => g.settled)
// 自动结算：卖单成本已经说得清（手填单件成本 / 标了无成本 / 两侧都填），
// 不用包联单、也不用手动完结，后端统计时直接计入「已结算」。
// 判定口径要和后端 autoSettled 一致：改一处就同步改另一处。
const autoSettled = (r) => !!r && !r.settled_at && !r.discarded_at && !groupSettled(r) &&
  r.has_sell && r.source !== '寄卖' && r.source !== '代采' &&
  ((r.unit_cost || 0) > 0 || r.cost_free || (r.buy_price || 0) > 0)
// 弃单：记录还在，但不占在库、不计成本与利润
const isDiscarded = (r) => !!(r && r.discarded_at)
// 没有成本单的卖单（独立卖单，没有买进侧）：不占资金也没有成本，
// 所以不提供「完结」——完结对它没有意义，只会让数字看着像出库了。
const noCostSale = (r) => !!r && r.kind === 'sale' && !r.parent_id && !r.has_buy && !r.discarded_at
// 卖单费用为 0 且到手价等于成交价：闲鱼账单还没生成（次月/3 天后出），不是真的免手续费
const feePending = (r) => !!r.has_sell && !r.sell_fee && !r.sell_wuyou_fee && Math.abs((r.sell_net || 0) - (r.sell_price || 0)) < 0.005
// 闲鱼同步但账单还没出：列表接口给的是「商品金额」，账单出来后才会按「实收金额」更正。
// 只在同步记录上标，手动录入的卖单没费用不代表待结算。
const billPending = (r) => feePending(r) && /账单尚未生成/.test(r.note || '')
// 属于联单的记录不能在明细里单独完结/弃单，必须回联单页操作
const inGroup = (r) => (groupsOf(r.id) || []).length > 0
// 已完结联单里的记录等于「已对过账」，不让直接改/删，必须先回联单页重新开启
const settledTitleOf = (r) => (groupsOf(r.id) || []).filter(g => g.settled).map(g => g.title || '未命名联单')[0] || '未命名联单'
// 单商品完结：不走联单，直接把这一条结案。
// 完结后不进在库、不出现在联单候选，也能随时取消。

// 不能完结的原因（返回空串=可以完结）。
// 以前是直接把按钮 disabled 掉，用户只看到灰按钮、不知道该怎么处理；
// 现在按钮始终可点，点下去明确告诉去哪儿解除。
function discardBlock(r) {
  if (r.settled_at) return '这条已单独完结，先点「取消完结」再弃单'
  const g = (groupsOf(r.id) || [])[0]
  if (g) return `这条在联单「${g.title || '未命名联单'}」里，请先在「联单」页把它移出联单，再标记弃单`
  return ''
}

// 弃单：这单作废，记录保留但不计任何钱
const discarding = ref(0)
async function toggleDiscard(r) {
  if (discarding.value) return
  const blocked = discardBlock(r)
  if (blocked) {
    emit('notify', blocked, true)
    return
  }
  const on = !r.discarded_at
  const ok = await confirm(on ? {
    title: '标记为弃单？',
    message: `「${r.product || '未命名商品'}」将作废：不占在库、不计成本与利润率，相当于没录过，但记录仍保留可查。`,
    confirmText: '弃单'
  } : {
    title: '取消弃单？',
    message: `「${r.product || '未命名商品'}」将恢复正常，重新计入在库、成本与利润率。`,
    confirmText: '取消弃单'
  })
  if (!ok) return
  discarding.value = r.id
  try {
    await api.discardRecord(r.id, on)
    emit('notify', on ? '已标记为弃单' : '已取消弃单')
    await load()
  } catch (e) {
    emit('notify', e.message || (on ? '操作失败' : '操作失败'), true)
  } finally {
    discarding.value = 0
  }
}

function editRecord(r) {
  if (blockIfSettled(r, '编辑')) return
  emit('edit', r)
}
function blockIfSettled(r, action = '修改') {
  if (!groupSettled(r)) return false
  emit('notify', `「${settledTitleOf(r)}」是已完结联单，${action}前请先在「联单」页把它重新开启`, true)
  return true
}
// 钢印上的日期：完结/弃单发生在什么时候。
// 单独完结的用记录自己的时间；整张联单完结的，回落到联单的完结时间。
const sealDate = (r, t) => {
  const s = String(t || '')
  if (s.length >= 10) return s.slice(0, 10).replace(/-/g, '.')
  const g = (groupsOf(r && r.id) || []).find((x) => x.settled) || {}
  return String(g.settled_at || '').slice(0, 10).replace(/-/g, '.')
}
const typeLabel = (r) => isDiscarded(r) ? '弃单' : groupSettled(r) ? '完结单' : (r.has_sell ? '卖单' : '买单')
// 详情面板里的结算状态：让「为什么这单算已结算」一目了然
const settleStateOf = (r) => {
  if (isDiscarded(r)) return '已作废（不进账）'
  if (groupSettled(r)) return '已结算 · 联单完结'
  if (autoSettled(r)) return r.cost_free ? '已结算 · 标记为无成本' : '已结算 · 成本已明确'
  if (!r.has_sell) return '不适用（还没卖出）'
  return '未结算 · 还没填成本'
}
const typeClass = (r) => isDiscarded(r) ? 'badge-discard' : groupSettled(r) ? 'badge-settled' : (r.has_sell ? 'badge-sell' : 'badge-buy')
// 每行「买进价 / 到手价」按你的口径：
// 第一格：卖出单看卖价、买进单看买价（它们都是这笔的「成交价」）
// 第一格：纯买进单看买进价，其余（卖单/两侧都填/弃单）看卖出价。
// 注意不能用 kind === 'purchase' 判断——弃单后 kind 会变成 discarded，
// 弃掉的买进单会被误判成卖单、金额显示成 0。改用 has_sell 判断更稳。
// 单件成本的展示文案：手填的优先；没手填但联单有买进单，用分摊下来的成本；都没有就说没填。
// 时间/昵称都为空的记录（纯买单常见）不渲染副标题行，否则会剩一个孤零零的「—」
const hasMeta = (r) => !!(r && (r.deal_time || (r.has_sell ? r.sell_counterparty : r.buy_counterparty)))
const unitCostText = (r) => {
  const own = Number(r.unit_cost) || 0
  if (own > 0) return fmtMoney(own)
  const alloc = Number(r.alloc_cost) || 0
  if (alloc > 0) return fmtMoney(alloc) + '(联单分摊)'
  return '未填'
}
const rowBuyPrice = (r) => (r.has_sell ? (r.sell_price || 0) : (r.buy_price || 0))
const rowNet = (r) => ((r.sell_price || 0) - (r.sell_shipping || 0) - (r.sell_fee || 0) - (r.sell_wuyou_fee || 0))
// 中间格：卖单才是「到手价」；买单没有到手价，看的是总成本（买价 + 买运费）。
// 以前买单这里放的是「买价 − 买运费」还标成到手价，既扣错方向也和汇总口径对不上。
const rowMidLabel = (r) => (r.has_sell ? '到手价' : '总成本')
const rowMidValue = (r) => (r.has_sell ? rowNet(r) : (r.cost || 0))
// 卖单第三格：卖运费 + 软件服务费 + 无忧卖，正好是「卖出价 → 到手价」的差额
const sellDeduct = (r) => (r.sell_shipping || 0) + (r.sell_fee || 0) + (r.sell_wuyou_fee || 0)
// 与录入端一致：超过 0.1 元才算真的付了运费
const hasShipFee = (v) => (Number(v) || 0) > 0.1

// 列选择面板：点击面板外自动收起
function onDocClick(e) {
  if (!colPanelOpen.value) return
  if (!(e.target instanceof Element) || !e.target.closest('.col-picker')) colPanelOpen.value = false
}
onMounted(() => {
  // 从商品利润排行跳进来时，focusNonce 的 immediate watch 已经发过请求了，别重复发
  if (!productFilter.value) load()
  loadTodo()
  document.addEventListener('click', onDocClick)
})
onUnmounted(() => document.removeEventListener('click', onDocClick))
watch(() => props.refreshKey, () => { load(); loadTodo() })
watch([type, from, to, sort, order, pageSize], () => { if (!jumping) reload(true) })
// 金额逐键触发会打出一串请求：400ms 内只发最后一次
let amountTimer = null
watch([minAmount, maxAmount], () => {
  clearTimeout(amountTimer)
  amountTimer = setTimeout(() => reload(true), 400)
})
// 手动改日期时取消预设 chip 高亮（applyRange 内部设置时跳过）
watch([from, to], () => { if (!settingRange) activeRange.value = -1 })
watch([sort, order], () => {
  try { localStorage.setItem('xy_sort', JSON.stringify({ sort: sort.value, order: order.value })) } catch (e) {}
})
watch(showCols, (v) => {
  try { localStorage.setItem('xy_columns', JSON.stringify(v)) } catch (e) {}
}, { deep: true })
const batchBusy = ref(false)
async function applyBatchEdit() {
  if (batchBusy.value) return
  if (!selected.value.length) {
    emit('notify', '请先勾选要修改的记录', true)
    return
  }
  if (!batchValue.value.trim()) {
    emit('notify', '请先填写要改成的新值', true)
    return
  }
  const locked = items.value.filter(i => selected.value.includes(i.id) && groupSettled(i))
  if (locked.length) {
    emit('notify', `选中的记录里有 ${locked.length} 条属于已完结联单，请先在「联单」页重新开启后再改`, true)
    return
  }
  batchBusy.value = true
  try {
    const r = await api.batchUpdate(selected.value, { [batchField.value]: batchValue.value.trim() })
    emit('notify', r.updated > 0 ? `已更新 ${r.updated} 条记录` : '没有记录被修改（该字段可能不允许批量改）')
    batchValue.value = ''
    load()
  } catch (e) { emit('notify', '批量修改失败：' + e.message, true) }
  finally { batchBusy.value = false }
}
function exportProfit() {
  runExport('profit', api.exportUrl('profit'), `利润表-${todayStr()}.csv`, '已导出利润表')
}
function exportColumns() {
  runExport('columns', api.exportUrl('csv', ['product', 'buy_price', 'sell_price', 'sell_net', 'note']),
    `闲鱼明细-常用列-${todayStr()}.csv`, '已导出常用列')
}
let touchTimer = null
let touchMoved = false
let suppressClick = false
const pressingId = ref(null)

// 移动端左滑快捷操作：与既有「长按选中」共存。
// 判定为横向手势才接管（纵向滚动/长按不受影响），一次只展开一行。
const SWIPE_W = 96
const swipeId = ref(null)     // 已吸附展开的行
const swipingId = ref(null)   // 正在跟手拖动的行
const swipeX = ref(0)
let startX = 0, startY = 0, activeRecord = null
function closeSwipe() { swipeId.value = null; swipingId.value = null; swipeX.value = 0 }

function onTouchStart(r, e) {
  activeRecord = r
  const t = e?.touches?.[0]
  startX = t ? t.clientX : 0
  startY = t ? t.clientY : 0
  pressingId.value = r.id
  touchMoved = false
  suppressClick = false
  if (swipeId.value && swipeId.value !== r.id) closeSwipe()  // 点/滑别处时收起上一行
  clearTimeout(touchTimer)
  touchTimer = setTimeout(() => {
    if (touchMoved || swipingId.value) return
    suppressClick = true          // 拦截长按后紧随的 click，避免「选中 + 展开/编辑」同时发生
    if (!selectMode.value) {
      selectMode.value = true     // 长按进入选择模式：所有卡片一起露出勾选框
      emit('notify', '已进入多选：点卡片即可选择，再点一下取消', false, null, { tone: 'info' })
    }
    if (!selected.value.includes(r.id)) selected.value.push(r.id)
    else selected.value = selected.value.filter((x) => x !== r.id)
    try { navigator.vibrate?.(8) } catch (e) { /* ignore */ }
    // 兜底：正常情况由 touchend 解除防误触标记；万一 touchend 丢了（部分内核/手势被系统截走），
    // 这里也定时解除，避免之后所有点击都被吞掉。
    clearTimeout(touchTimer)
    setTimeout(() => { suppressClick = false }, 600)
  }, 500)
}
function onTouchMove(e) {
  const t = e?.touches?.[0]
  if (!t) return
  const dx = t.clientX - startX
  const dy = t.clientY - startY
  if (swipingId.value !== activeRecord?.id) {
    // 横向且明显大于纵向才算滑动；否则交给页面滚动 / 长按逻辑
    if (window.innerWidth <= 600 && Math.abs(dx) > 10 && Math.abs(dx) > Math.abs(dy) * 1.2) {
      swipingId.value = activeRecord?.id ?? null
      clearTimeout(touchTimer)
      pressingId.value = null
      suppressClick = true
    } else {
      touchMoved = true
      pressingId.value = null
      clearTimeout(touchTimer)
      return
    }
  }
  if (!swipingId.value) return
  e.preventDefault()
  const base = swipeId.value === swipingId.value ? -SWIPE_W : 0
  swipeX.value = Math.max(-SWIPE_W - 18, Math.min(0, base + dx))
}
function onTouchEnd() {
  clearTimeout(touchTimer)
  pressingId.value = null
  if (swipingId.value) {
    const open = swipeX.value < -SWIPE_W / 2
    swipeId.value = open ? swipingId.value : null
    swipeX.value = open ? -SWIPE_W : 0
    swipingId.value = null
    suppressClick = true
  }
  if (suppressClick) setTimeout(() => { suppressClick = false }, 400)
}
// 卡片点击：选择模式下整张卡就是勾选区域（不用再点那个小方框），
// 普通模式下才是展开/收起详情。长按或滑动刚结束时忽略这次 click。
// 「查看详情」是自己的按钮，不能走上面那条「点到 button 就放行」的分支，
// 否则点击会被直接 return 掉，表现为按钮点了没反应。
function toggleDetail(r) {
  if (suppressClick) return
  if (selectMode.value) {
    toggleOne(r.id)
    try { navigator.vibrate?.(5) } catch (e) { /* ignore */ }
    return
  }
  expanded.value = expanded.value === r.id ? null : r.id
}

function onCardClick(r, e) {
  if (suppressClick) return
  if (swipeId.value) { closeSwipe(); return }
  // 点按钮、勾选框、联单 chip、截图等交互元素时让它们自己处理
  const t = e && e.target
  if (t && t.closest && t.closest('button, input, label, a, .group-chip')) return
  if (selectMode.value) {
    toggleOne(r.id)
    try { navigator.vibrate?.(5) } catch (e) { /* ignore */ }
    return
  }
  expanded.value = expanded.value === r.id ? null : r.id
}
</script>

<template>
  <div class="page">
    <div v-if="error" class="alert-err">{{ error }} <button class="btn btn-sm" @click="load">重试</button></div>

    <!-- 底部导航「明细」那个红点的说明：点进去能直接只看这些单 -->
    <div v-if="todoUnlinked > 0 && type !== 'unlinked'" class="todo-hint">
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
      <span>有 <b>{{ todoUnlinked }}</b> 笔卖单还没挂成本，利润暂时按 0 算</span>
      <button class="btn btn-sm" @click="type = 'unlinked'">只看这些</button>
      <button class="btn btn-sm btn-quiet" @click="openAutoLink">一键挂成本</button>
    </div>

    <!-- 筛选：三行分区，主操作 / 条件 / 工具各占一层 -->
    <section class="card filter-card" :class="{ 'filter-open': filterOpen }">
      <div class="filter-main">
        <div class="search-wrap">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input class="input" v-model="q" @input="onSearchInput" placeholder="搜索商品 / 订单号 / 昵称 / 备注 / 时间" />
          <button v-if="q" class="search-clear" @click="clearSearch" aria-label="清除搜索">×</button>
        </div>
        <button class="btn btn-sm btn-quiet filter-toggle" @click="filterOpen = !filterOpen" :aria-expanded="filterOpen">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6h16M7 12h10M10 18h4"/></svg>
          筛选<span v-if="activeFilterCount" class="ft-count">{{ activeFilterCount }}</span>
        </button>
        <button class="btn btn-primary filter-add" @click="emit('add')">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
          录入记录
        </button>
      </div>

      <div class="filter-body">
        <div class="filter-body-in">
          <div class="filter-row">
            <div class="filter-group">
              <span class="fg-label">类型</span>
              <div class="chips">
                <button v-for="o in typeOptions" :key="o.v" class="chip" :class="{ on: type === o.v }" @click="type = o.v"
                      :title="o.v === 'unlinked' ? '既没挂买进单、也不在含买进单的联单里、且没标「无成本」的卖单（成本只能按 0 计）' : ''">{{ o.t }}</button>
              </div>
            </div>
            <span class="fg-divider"></span>
            <div class="filter-group">
              <span class="fg-label">时间</span>
              <div class="chips range-chips">
                <button v-for="(r, i) in rangePresets" :key="r.t" class="chip" :class="{ on: activeRange === i }" @click="applyRange(r.days, i)">{{ r.t }}</button>
              </div>
            </div>
            <span v-if="hasFilters" class="filter-active">
              已筛选 {{ activeFilterCount }} 项 · <span class="link" @click="clearFilters">清除</span>
            </span>
          </div>

          <div class="filter-foot">
            <div class="date-range">
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="16" rx="2"/><line x1="3" y1="10" x2="21" y2="10"/><line x1="8" y1="3" x2="8" y2="7"/><line x1="16" y1="3" x2="16" y2="7"/></svg>
              <input class="input date-input" type="date" v-model="from" title="开始日期" />
              <span class="muted">至</span>
              <input class="input date-input" type="date" v-model="to" title="结束日期" />
            </div>
            <div class="amount-range">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v10M9 10h6M9 14h6"/></svg>
              <input class="input amount-input" type="number" min="0" step="0.01" v-model="minAmount"
                     placeholder="最低价" title="价格区间下限：买进价 / 卖出价 / 到手价 / 成本 任一项落在区间内即命中"
                     @input="onSearchInput" @keyup.enter="reload(true)" />
              <span class="muted">—</span>
              <input class="input amount-input" type="number" min="0" step="0.01" v-model="maxAmount"
                     placeholder="最高价" title="价格区间上限"
                     @input="onSearchInput" @keyup.enter="reload(true)" />
            </div>
            <div class="sort-group">
              <select class="select sort-select" v-model="sort" title="排序字段">
                <option v-for="o in sortOptions" :key="o.v" :value="o.v">{{ o.t }}</option>
              </select>
              <button class="btn btn-sm btn-quiet" @click="order = order === 'desc' ? 'asc' : 'desc'" :title="order === 'desc' ? '当前降序，点击切升序' : '当前升序，点击切降序'">
                {{ order === 'desc' ? '↓ 降序' : '↑ 升序' }}
              </button>
              <button class="btn btn-sm btn-ghost bi-entry" @click="emit('batch')" title="一次导入多张订单截图">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 16V4"/><path d="M7 9l5-5 5 5"/><path d="M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/></svg>
                批量导入
              </button>
              <button class="btn btn-sm btn-ghost" @click="exportCsv" :disabled="!!exporting" title="导出当前筛选结果为 CSV">
                <span v-if="exporting === 'csv'" class="spin"></span>{{ exporting === 'csv' ? '导出中' : '导出 CSV' }}
              </button>
              <button class="btn btn-sm btn-ghost" @click="exportColumns" :disabled="!!exporting" title="导出常用列">
                <span v-if="exporting === 'columns'" class="spin"></span>{{ exporting === 'columns' ? '导出中' : '自定义列' }}
              </button>
              <button class="btn btn-sm btn-ghost" @click="exportProfit" :disabled="!!exporting" title="导出利润表">
                <span v-if="exporting === 'profit'" class="spin"></span>{{ exporting === 'profit' ? '导出中' : '利润表' }}
              </button>
              <button class="btn btn-sm btn-ghost" @click="clearFilters">重置</button>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 汇总：6 张卡填满网格，净利润按中国习惯正红负绿 -->
    <div class="section-caption sum-caption" @click="sumOpen = !sumOpen" :aria-expanded="sumOpen"
         title="手机上点这里展开 / 收起六项汇总">
      <span>数据汇总</span><small>按当前筛选条件统计</small>
      <span class="sum-peek">{{ fmtNum(total) }} 条 · 净利 <b class="num" :class="filteredProfit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(filteredProfit) }}</b></span>
      <svg class="sum-chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
    </div>
    <div class="sum-grid" :class="{ 'sum-collapsed': !sumOpen }">
      <div class="card sum-card sum-total"><div class="m-label">筛选结果</div><div class="m-value num"><AnimatedNumber :value="total" v-slot="{ value }">{{ fmtNum(Math.round(value)) }}</AnimatedNumber><small>条记录</small></div></div>
      <div class="card sum-card"><div class="m-label">销售额</div><div class="m-value num"><AnimatedNumber :value="sum?.total_sales || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div></div>
      <div class="card sum-card" title="总成本 = 货品成本 + 运费 + 软件服务费 + 无忧卖服务费"><div class="m-label">总成本</div><div class="m-value num"><AnimatedNumber :value="sum?.total_cost || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div></div>
      <div class="card sum-card"><div class="m-label">运费支出</div><div class="m-value num"><AnimatedNumber :value="sum?.total_shipping || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div></div>
      <div class="card sum-card"><div class="m-label">服务费合计</div><div class="m-value num"><AnimatedNumber :value="sum?.total_fee || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber><small>含无忧卖 {{ fmtMoney(sum?.total_wuyou_fee || 0) }}</small></div></div>
      <div class="card sum-card sum-profit" title="累计净利润含在售盈亏；仅统计已完结联单的口径见概览首卡"><div class="m-label">净利润</div><div class="m-value num" :class="filteredProfit >= 0 ? 'pos' : 'neg'"><AnimatedNumber :value="filteredProfit" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div></div>
    </div>

    <!-- 批量操作条 -->
    <div v-if="selected.length" class="bulk-bar">
      <span class="bb-ico" aria-hidden="true"><svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.4" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12l4.5 4.5L19 7"/></svg></span>
      <span class="bb-text">已选 <b>{{ selected.length }}</b> 单</span>
      <div class="bulk-edit">
        <select class="select" v-model="batchField" title="要批量修改的字段">
          <option value="note">备注</option><option value="buy_shipping">买运费金额</option>
          <option value="sell_shipping">卖运费金额</option><option value="buy_ship_tag">买运费标签</option>
          <option value="sell_ship_tag">卖运费标签</option><option value="buy_time">买进时间</option>
          <option value="sell_time">卖出时间</option>
        </select>
        <input class="input" v-model="batchValue" placeholder="填入新值" @keyup.enter="applyBatchEdit" />
        <button class="btn btn-sm btn-primary" :disabled="batchBusy" @click="applyBatchEdit">
          <span v-if="batchBusy" class="spin"></span>应用
        </button>
      </div>
      <button class="btn btn-sm" @click="openGroupPicker(selected)">加入联单</button>
      <button class="btn btn-sm" :disabled="costBusy" @click="openCostPicker">
        <span v-if="costBusy" class="spin"></span>挂成本
      </button>
      <button class="btn btn-sm" :disabled="!canMarkFree || costFreeBusy"
              :title="canMarkFree ? '这单本来就没有货品成本（代卖 / 寄卖 / 赠送），标记后不再提示挂成本' : '只有没挂买进单的独立卖单才能标'"
              @click="toggleCostFree">
        <span v-if="costFreeBusy" class="spin"></span>{{ allCostFree ? '取消无成本' : '无成本' }}
      </button>
      <button class="btn btn-sm btn-quiet" @click="selected = []; selectMode = false; emit('notify', '已退出多选', false, null, { tone: 'info' })">取消选择</button>
      <button class="btn btn-sm btn-danger" @click="batchDel">批量删除</button>

    </div>

    <!-- 批量挂成本：给没有成本单的卖单选一个买进单，按件数分摊单件成本。
         挂到 body 上，避免被批量操作条的容器挤成窄条。 -->
    <Teleport to="body">
      <div v-if="costOpen" class="cp-mask" @click.self="costOpen = false">
        <div class="cost-picker">
          <div class="cp-head">把选中的 <b>{{ selected.length }}</b> 张卖单挂到买进单</div>
          <input class="input" v-model="costQ" placeholder="搜索商品 / 订单号 / 卖家" />
          <div class="cp-list">
            <button v-for="p in costCandidates" :key="p.id" type="button" class="cp-item" @click="linkTo(p)">
              <span class="cp-name">{{ p.product || '未命名商品' }}</span>
              <span class="cp-meta">单件成本 {{ fmtMoney(unitOf(p)) }} · {{ p.quantity || 1 }} 件 · 已挂 {{ p.child_count || 0 }} 单</span>
            </button>
            <div v-if="!costCandidates.length" class="muted cp-empty">没有匹配的买进单</div>
          </div>
          <div class="cp-foot">
            <button class="btn btn-sm btn-quiet cp-nocost" :disabled="costFreeBusy" @click="markNoCostFromPicker"
                    title="代卖 / 寄卖 / 赠送这类本来就没有货品成本的单，不用挑买进单">
              这些单本来就没有成本
            </button>
            <button class="btn btn-sm btn-quiet" @click="costOpen = false">取消</button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- 智能挂成本：先把匹配结果摆出来，用户逐条确认后再写库 -->
    <Teleport to="body">
      <div v-if="autoOpen" class="cp-mask" @click.self="autoOpen = false">
        <div class="auto-link">
          <div class="al-head">
            <div>
              <b>{{ estimateMode ? '按售价估算成本' : '智能挂成本' }}</b>
              <span class="muted">{{ estimateMode ? '没有买进单的卖单，按售价比例直接填成本' : '按商品名把未挂成本的卖单配到买进单' }}</span>
            </div>
            <button class="btn btn-sm btn-quiet" aria-label="关闭" @click="autoOpen = false">✕</button>
          </div>

          <div v-if="!autoLoading && !estimateMode" class="al-stat">
            <span class="al-chip ok">可直接挂 {{ autoStat.matched }}</span>
            <span class="al-chip warn">待确认 {{ autoStat.ambiguous }}</span>
            <span class="al-chip">无买进单 {{ autoStat.unmatched }}</span>
            <span class="al-spacer"></span>
            <button class="link" @click="autoToggleAll(true)">全选可挂</button>
            <button class="link" @click="autoToggleAll(false)">全不选</button>
          </div>

          <div v-if="autoLoading" class="al-loading"><span class="spin"></span> 正在匹配…</div>
          <div v-else-if="estimateMode" class="al-list">
            <div class="al-pct">
              <span>按售价的</span>
              <input class="input num al-pct-input" type="number" min="0" max="100" step="1" v-model.number="estimatePct" />
              <span>% 作为进价</span>
              <button class="btn btn-sm btn-quiet" @click="applyPct">重算全部</button>
              <span class="muted">可以逐条改，只有勾选的会写入</span>
            </div>
            <div v-for="r in estimateRows" :key="'e' + r.sale_id" class="al-row">
              <input type="checkbox" v-model="r._pick" />
              <div class="al-main">
                <div class="al-name cell-ellipsis" :title="r.product">{{ r.product || '未命名商品' }}</div>
                <div class="al-meta muted">售价 {{ fmtMoney(r.sale_price) }}</div>
              </div>
              <div class="al-cost">
                <span class="muted">成本</span>
                <input class="input num al-cost-input" type="number" step="0.01" v-model.number="r._cost" />
              </div>
            </div>
          </div>
          <div v-else-if="!autoRows.length" class="al-empty muted">没有未挂成本的卖单，不用处理。</div>
          <div v-else class="al-list">
            <div v-for="r in autoPickable" :key="r.sale_id" class="al-row">
              <input type="checkbox" v-model="r._pick" />
              <div class="al-main">
                <div class="al-name cell-ellipsis" :title="r.product">{{ r.product || '未命名商品' }}</div>
                <div class="al-meta muted">
                  售价 {{ fmtMoney(r.sale_price) }}<span v-if="r.sale_time"> · {{ shortTime(r.sale_time) }}</span>
                  <span v-if="r.reason === 'ambiguous'" class="al-flag">需确认</span>
                </div>
              </div>
              <select v-model.number="r._purchase" class="select al-sel">
                <option v-for="c in r.candidates" :key="c.purchase_id" :value="c.purchase_id">
                  {{ c.product }} · 单价 {{ fmtMoney(c.unit_cost) }} · 剩 {{ c.left }} 件{{ c.match === 'similar' ? '（相似）' : '' }}
                </option>
              </select>
            </div>

            <button v-if="autoUnmatched.length" class="al-more" @click="showUnmatched = !showUnmatched">
              {{ showUnmatched ? '收起' : `另有 ${autoUnmatched.length} 条找不到可配的买进单，展开查看` }}
            </button>
            <template v-if="showUnmatched">
              <div v-for="r in autoUnmatched" :key="r.sale_id" class="al-row off">
                <input type="checkbox" disabled />
                <div class="al-main">
                  <div class="al-name cell-ellipsis" :title="r.product">{{ r.product || '未命名商品' }}</div>
                  <div class="al-meta muted">售价 {{ fmtMoney(r.sale_price) }}<span v-if="r.sale_time"> · {{ shortTime(r.sale_time) }}</span></div>
                </div>
                <span class="al-none muted">
                  {{ r.reason === 'no_stock' ? '同名买进单件数已用完' : '没有可配的买进单' }}
                </span>
              </div>
            </template>
          </div>

          <div v-if="estimateMode" class="cp-foot al-foot">
            <span class="muted">估算只是把利润算得接近真实，随时能在编辑里改</span>
            <button class="btn btn-sm btn-quiet" @click="estimateMode = false">返回挂接</button>
            <button class="btn btn-sm btn-primary" :disabled="autoBusy || !estimatePicked.length" @click="submitEstimate">
              <span v-if="autoBusy" class="spin"></span>确认填成本 {{ estimatePicked.length }} 单
            </button>
          </div>
          <div v-else class="cp-foot al-foot">
            <span class="muted" v-if="!autoPicked.length && autoPickable.length">勾选左侧记录后再确认，标「需确认」的先核对一下商品是否同一件</span>
            <span class="muted" v-else>单件成本 =（买价 + 买运费）/ 件数，挂完自动并入联单</span>
            <button v-if="autoUnmatched.length" class="btn btn-sm btn-quiet" @click="enterEstimate">
              这些单没有买进单，按售价估算成本
            </button>
            <button class="btn btn-sm btn-quiet" @click="autoOpen = false">取消</button>
            <button class="btn btn-sm btn-primary" :disabled="autoBusy || !autoPicked.length" @click="applyAutoLink">
              <span v-if="autoBusy" class="spin"></span>确认挂 {{ autoPicked.length }} 条
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <section ref="recordsCardRef" class="card records-card" :class="{ 'cost-flash': costFlash }">
      <div v-if="focusId" class="focus-bar">
        <span>已定位到指定记录</span>
        <button class="btn btn-sm btn-quiet" @click="clearFocus">显示全部</button>
      </div>
      <div v-if="productFilter" class="focus-bar product-bar">
        <span>只看商品「{{ productFilter.label }}」的单据（大小写 / 空格 / 标点不同的写法已合并）</span>
        <button class="btn btn-sm btn-quiet" @click="clearSearch">显示全部记录</button>
      </div>
      <div class="rc-headbar">
        <span>交易明细</span>
        <div class="rc-headbar-right">
          <span class="muted">共 {{ fmtNum(total) }} 条记录</span>
          <button class="btn btn-sm btn-quiet" @click="openAutoLink" title="按商品名自动匹配买进单，确认后再挂成本">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1"/><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1"/></svg>
            智能挂成本
          </button>
          <div class="col-picker">
            <button class="btn btn-sm btn-quiet" @click="colPanelOpen = !colPanelOpen" title="自定义显示列">列</button>
            <div v-if="colPanelOpen" class="col-panel">
              <label><input type="checkbox" v-model="showCols.shot" /> 截图</label>
              <label><input type="checkbox" v-model="showCols.shipping" /> 运费</label>
              <label><input type="checkbox" v-model="showCols.fee" /> 服务费</label>
              <label><input type="checkbox" v-model="showCols.time" /> 交易时间</label>
            </div>
          </div>
        </div>
      </div>

      <!-- 桌面表格 -->
      <div class="table-wrap desktop-only" :class="{ 'is-resorting': resorting }">
        <table class="rec-table" :class="{ 'hide-shot': !showCols.shot, 'hide-shipping': !showCols.shipping, 'hide-fee': !showCols.fee, 'hide-time': !showCols.time }">
          <thead>
            <tr>
              <th class="col-check"><input type="checkbox" :checked="allChecked" @change="toggleAll" /></th>
              <th>商品 / 订单</th>
              <th class="col-shot">截图</th>
              <th class="th-right" title="卖单取卖出成交价，买单取买进价">成交价</th>
              <th class="th-right col-ship" title="买运费 + 卖运费">运费</th>
              <th class="th-right" title="卖单为到手价（卖价−运费−服务费）；买单为总成本（买价+运费）">到手价 / 成本</th>
              <th class="th-right col-fee" title="软件服务费 + 无忧卖服务费">服务费</th>
              <th class="col-time">交易时间</th>
              <th class="th-right">操作</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="r in items" :key="r.id">
              <tr :data-rid="r.id" :class="{ selected: selected.includes(r.id), expandable: true, 'is-discarded': isDiscarded(r), 'row-flash': flashId === r.id, 'row-leaving': leaving.includes(r.id) }" @click="expanded = expanded === r.id ? null : r.id">
                <td class="col-check" @click.stop><input type="checkbox" :checked="selected.includes(r.id)" @change="toggleOne(r.id)" /></td>
                <td class="cell-main">
                  <span v-if="isDiscarded(r)" class="seal seal-row seal-discard" aria-hidden="true">
                    <span class="seal-inner">
                      <span class="seal-star">★ ★ ★</span>
                      <span class="seal-main">已弃单</span>
                      <span class="seal-sub">{{ sealDate(r, r.discarded_at) }}</span>
                    </span>
                  </span>
                  <span v-else-if="groupSettled(r)" class="seal seal-row seal-settled" aria-hidden="true">
                    <span class="seal-inner">
                      <span class="seal-star">★ ★ ★</span>
                      <span class="seal-main">已完结</span>
                      <span class="seal-sub">{{ sealDate(r, r.settled_at) }}</span>
                    </span>
                  </span>
                  <div class="cell-product cell-ellipsis" :title="r.product">
                    <span class="badge mini rc-kind" :class="r.has_sell ? 'badge-sell' : 'badge-buy'">{{ r.has_sell ? '卖单' : '买单' }}</span>
                    {{ r.product || '未命名商品' }}
                  </div>
                  <div class="cell-meta">
                    <span class="cell-sub">{{ counterpartyOf(r) || '—' }}</span>
                    <span v-if="isDiscarded(r)" class="discard-dot">已弃单</span>
                    <span v-if="r.source === '寄卖'" class="consign-tag" :title="'寄卖单' + (r.owner ? ' · 归属 ' + r.owner : '')">寄卖单</span>
                    <span v-if="r.source === '代采'" class="consign-tag daicai-tag" :title="'代采单' + (r.owner ? ' · 替 ' + r.owner + ' 买' : '')">代采单</span>
                    <span v-if="r.owner && r.source !== '寄卖' && r.source !== '代采'" class="owner-chip" :title="'归属：' + r.owner">{{ r.owner }}</span>
                    <span v-if="groupSettled(r)" class="settled-dot" title="已完结联单内的订单">✓ 已完结</span>
                    <span v-else-if="autoSettled(r)" class="auto-settled-chip" title="成本已明确，自动计入已结算（不用包联单）">已结算</span>
                    <span v-if="r.cost_free && r.source !== '寄卖' && r.source !== '代采'" class="costfree-dot" title="已标记：这单没有货品成本（代卖 / 寄卖 / 赠送）">无成本</span>
                    <span v-if="groupsOf(r.id).length" class="cell-groups">
                      <span v-for="g in groupsOf(r.id).slice(0, 2)" :key="'g'+g.id" class="group-chip" @click.stop="emit('goto-group', g.id)" :title="'查看联动单：'+g.title">
                        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg>
                        {{ g.title }}
                      </span>
                      <span v-if="groupsOf(r.id).length > 2" class="group-more">+{{ groupsOf(r.id).length - 2 }}</span>
                    </span>
                  </div>
                </td>
                <td class="col-shot">
                  <button v-if="r.attachment" type="button" class="td-thumb" @click.stop="openImage(r)"
                          title="查看订单截图" aria-label="查看订单截图">
                    <img :src="thumbUrl(r, 160)" alt="订单截图" loading="lazy" decoding="async" />
                  </button>
                  <span v-else class="td-thumb-empty">—</span>
                </td>
                <td class="td-right num td-buy">
                  {{ fmtMoney(rowBuyPrice(r)) }}
                  <b v-if="billPending(r)" class="fee-pending"
                     title="闲鱼订单列表给的是商品金额；账单一般延后几天生成，出账后再点「立即同步」会按实收金额自动更正">待账单</b>
                </td>
                <td class="td-right num td-muted col-ship">{{ fmtMoney(r.buy_shipping + r.sell_shipping) }}</td>
                <td class="td-right num" :class="[r.has_sell ? 'net-col' : 'td-buy', { 'is-neg': r.has_sell && rowNet(r) < 0 }]">{{ fmtMoney(rowMidValue(r)) }}</td>
                <td class="td-right num td-muted col-fee">{{ r.has_sell ? fmtMoney((r.sell_fee || 0) + (r.sell_wuyou_fee || 0)) : '—' }}</td>
                <td class="muted nowrap cell-time col-time">{{ shortTime(r.deal_time) }}</td>
                <td class="td-right nowrap cell-actions rc-actions-desktop" @click.stop>
                  <button class="btn btn-sm btn-ghost btn-discard" :class="{ 'is-on': r.discarded_at }"
                          :disabled="!!discarding"
                          :title="r.discarded_at ? '已弃单，点此取消弃单' : (inGroup(r) ? '这条在联单里，请先移出联单再弃单' : '作废这一条：不占在库、不计成本与利润')"
                          @click="toggleDiscard(r)">
                    <svg class="ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2.5 11 13"/><path d="M21.5 2.5 15 21.5l-4-8.5-8.5-4z"/></svg>
                    {{ r.discarded_at ? '取消弃单' : '弃单' }}
                  </button>
                  <button class="btn btn-sm btn-ghost btn-group-add"
                          :disabled="groupSettled(r) || !!r.discarded_at"
                          :title="r.discarded_at ? '已弃单的记录不能加入联单' : (groupSettled(r) ? '所在联单已完结，请先在「联单」页重新开启' : '把这条记录加入某个联单')"
                          @click="openGroupPicker(r.id)">
                    <svg class="ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg>
                    联单
                  </button>
                  <button class="btn btn-sm btn-ghost" :disabled="groupSettled(r)" :title="groupSettled(r) ? '所在联单已完结，请先在「联单」页重新开启' : ''" @click="editRecord(r)">
                    <svg class="ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>
                    编辑
                  </button>
                  <button class="btn btn-sm btn-ghost btn-danger" :disabled="groupSettled(r)" :title="groupSettled(r) ? '所在联单已完结，请先在「联单」页重新开启' : ''" @click="del(r)">
                    <svg class="ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/></svg>
                    删除
                  </button>
                </td>
              </tr>
              <tr v-if="expanded === r.id" class="detail-row"><td :colspan="visibleColCount">
                <div class="detail-panel">
                  <div class="detail-group">
                    <div class="dg-title">订单信息</div>
                    <div class="dg-items">
                      <!-- 买单只列买侧、卖单只列卖侧，不把另一侧的空字段摆出来 -->
                      <div v-if="r.has_buy" class="dg-item"><span>买进订单号</span><b class="mono">{{ r.buy_order_no || '—' }}</b></div>
                      <div v-if="r.has_buy" class="dg-item"><span>卖家昵称</span><b>{{ r.buy_counterparty || '—' }}</b></div>
                      <div v-if="r.has_sell" class="dg-item"><span>卖出订单号</span><b class="mono">{{ r.sell_order_no || '—' }}</b></div>
                      <div v-if="r.has_sell" class="dg-item"><span>买家昵称</span><b>{{ r.sell_counterparty || '—' }}</b></div>
                    </div>
                  </div>
                  <div class="detail-group">
                    <div class="dg-title">资金与费用</div>
                    <div class="dg-items">
                      <div class="dg-item"><span>单件成本</span><b>{{ unitCostText(r) }}</b></div>
                      <div class="dg-item"><span>本条成本</span><b>{{ fmtMoney(r.cost || 0) }}</b></div>
                      <div v-if="r.has_buy" class="dg-item"><span>买运费 / 标签</span><b>{{ fmtMoney(r.buy_shipping) }} {{ r.buy_ship_tag || '' }}</b></div>
                      <div v-if="r.has_sell" class="dg-item"><span>卖运费 / 标签</span><b>{{ fmtMoney(r.sell_shipping) }} {{ r.sell_ship_tag || '' }}</b></div>
                      <div v-if="r.has_sell" class="dg-item"><span>软件服务费</span><b>{{ feePending(r) ? '待结算' : fmtMoney(r.sell_fee || 0) }}</b></div>
                      <div v-if="r.has_sell" class="dg-item"><span>无忧卖服务费</span><b>{{ fmtMoney(r.sell_wuyou_fee || 0) }}</b></div>
                    </div>
                  </div>
                  <div v-if="r.attachment" class="detail-group">
                    <div class="dg-title">订单截图</div>
                    <button type="button" class="dg-attach" @click.stop="openImage(r)">
                      <img :src="thumbUrl(r, 720)" alt="订单截图" loading="lazy" decoding="async" />
                      <span class="dg-attach-tip">点击查看大图</span>
                    </button>
                  </div>
                  <div class="detail-group">
                    <div class="dg-title">补充信息</div>
                    <div class="dg-items">
                      <div class="dg-item"><span>件数</span><b>{{ r.quantity || 1 }}</b></div>
                      <div class="dg-item"><span>货物来源</span><b>{{ r.source || '未标注' }}</b></div>
                      <div class="dg-item"><span>归属</span><b>{{ r.owner || '—' }}</b></div>
                      <div class="dg-item"><span>备注</span><b>{{ r.note || '—' }}</b></div>
                      <div v-if="r.parent_id" class="dg-item"><span>关联买进单</span><b>{{ parentProduct(r.parent_id) }}</b></div>
                      <div v-else-if="childCountOf(r.id)" class="dg-item"><span>已挂卖出</span><b>{{ childCountOf(r.id) }} 笔</b></div>
                    </div>
                  </div>
                  <div class="detail-group">
                    <div class="dg-title">单据状态</div>
                    <div class="dg-items">
                      <div class="dg-item"><span>结算</span><b>{{ settleStateOf(r) }}</b></div>
                      <div v-if="r.settled_at" class="dg-item"><span>完结于</span><b>{{ r.settled_at }}</b></div>
                      <div class="dg-item"><span>弃单</span><b>{{ r.discarded_at || '—' }}</b></div>
                    </div>
                    <div v-if="isDiscarded(r)" class="dg-hint">弃单不占在库、不计成本与利润率，只保留记录。</div>
                  </div>
                </div>
              </td></tr>
            </template>
          </tbody>
        </table>
      </div>

      <!-- 手机卡片：金额平铺，无需左右滑动 -->
      <div class="mobile-cards" :class="{ 'is-resorting': resorting }">
        <div v-for="r in items" :key="'m' + r.id" class="rec-swipe"
          :class="{ 'swiping': swipingId === r.id, 'swipe-open': swipeId === r.id }"
          @touchstart.capture.passive="onTouchStart(r, $event)" @touchmove.capture="onTouchMove"
          @touchend.capture.passive="onTouchEnd" @touchcancel.capture.passive="onTouchEnd">
          <div class="rec-swipe-actions" @touchstart.stop @click.stop>
            <button class="rsa-btn" :disabled="groupSettled(r)" @click="closeSwipe(); editRecord(r)" aria-label="编辑此记录" :title="groupSettled(r) ? '所在联单已完结，不可编辑' : '编辑'">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>
              <span>编辑</span>
            </button>
            <button class="rsa-btn rsa-danger" @click="closeSwipe(); del(r)" aria-label="删除此记录" title="删除">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/></svg>
              <span>删除</span>
            </button>
          </div>
          <div class="rec-card" :data-rid="r.id" :class="{ 'is-selected': selected.includes(r.id), 'is-pressing': pressingId === r.id, 'is-discarded': isDiscarded(r), 'row-flash': flashId === r.id, 'row-leaving': leaving.includes(r.id) }"
            :style="(swipingId === r.id || swipeId === r.id) ? { transform: 'translateX(' + swipeX + 'px)' } : null"
            @click="onCardClick(r, $event)">
          <div class="rc-head">
            <label v-if="selectMode" class="rc-check" @touchstart.stop @click.stop><input type="checkbox" :checked="selected.includes(r.id)" @change="toggleOne(r.id)" /></label>
            <div class="rc-title">
              <div class="rc-product">
                <span class="badge mini rc-kind" :class="r.has_sell ? 'badge-sell' : 'badge-buy'">{{ r.has_sell ? '卖单' : '买单' }}</span>
                {{ r.product || '未命名商品' }}
              </div>
              <div v-if="hasMeta(r)" class="rc-meta muted">
                <span v-if="r.deal_time" class="rc-time">{{ shortTime(r.deal_time) }}</span>
                <template v-if="counterpartyOf(r)">
                  <span class="rc-sep">·</span>
                  <span class="rc-name">{{ counterpartyOf(r) }}</span>
                </template>
              </div>
            </div>
            <!-- 手机端不在卡片上放缩略图：一是不占版面，二是不必为列表加载图片，
                 需要看时在详情里点「查看订单截图」 -->
            <span class="badge-stack">
              <!-- 三个圆章都跟随布局排在这里（不再 absolute 浮在卡片上），
                   位置固定在右上角徽章区，不会压住标题、金额或按钮 -->
              <span v-if="isDiscarded(r)" class="seal seal-inline seal-discard" :title="'弃单于 ' + sealDate(r, r.discarded_at)">
                <span class="seal-inner">
                  <span class="seal-star">★ ★ ★</span>
                  <span class="seal-main">已弃单</span>
                  <span class="seal-sub">{{ sealDate(r, r.discarded_at) }}</span>
                </span>
              </span>
              <span v-else-if="groupSettled(r)" class="seal seal-inline seal-settled" :title="'联单完结于 ' + sealDate(r, r.settled_at)">
                <span class="seal-inner">
                  <span class="seal-star">★ ★ ★</span>
                  <span class="seal-main">已完结</span>
                  <span class="seal-sub">{{ sealDate(r, r.settled_at) }}</span>
                </span>
              </span>
              <span v-else-if="r.source === '寄卖'" class="seal seal-inline seal-consign" :title="'寄卖单' + (r.owner ? ' · ' + r.owner : '')">
                <span class="seal-inner">
                  <span class="seal-star">★ ★ ★</span>
                  <span class="seal-main">寄卖单</span>
                  <span class="seal-sub">{{ r.owner || '代卖' }}</span>
                </span>
              </span>
              <span v-else-if="r.source === '代采'" class="seal seal-inline seal-daicai" :title="'代采单' + (r.owner ? ' · 替 ' + r.owner + ' 买' : '')">
                <span class="seal-inner">
                  <span class="seal-star">★ ★ ★</span>
                  <span class="seal-main">代采单</span>
                  <span class="seal-sub">{{ r.owner || '代买' }}</span>
                </span>
              </span>
              <span v-else-if="inGroup(r)" class="seal seal-inline seal-group"
                    :title="'已在联单「' + (groupsOf(r.id)[0].title || '未命名联单') + '」中，联单完结后一起结算'">
                <span class="seal-inner">
                  <span class="seal-star">★ ★ ★</span>
                  <span class="seal-main">联单中</span>
                  <span class="seal-sub">{{ sealDate(r, r.discarded_at) }}</span>
                </span>
              </span>
            </span>
          </div>
          <div v-if="groupsOf(r.id).length" class="rc-groups">
            <span v-for="g in groupsOf(r.id).slice(0, 2)" :key="'mg'+g.id" class="group-chip" @touchstart.stop @click="emit('goto-group', g.id)">
              <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg>
              {{ g.title }}
            </span>
            <span v-if="groupsOf(r.id).length > 2" class="group-more">+{{ groupsOf(r.id).length - 2 }}</span>
          </div>
          <div class="rc-amounts">
            <div class="rc-amt"><span>{{ r.has_sell ? '卖出价' : '买进价' }}</span><b class="num" :title="billPending(r) ? '商品金额，账单出来后按实收更正' : ''">{{ fmtMoney(rowBuyPrice(r)) }}</b></div>
            <div class="rc-amt" :class="{ 'rc-amt-primary': r.has_sell, 'is-neg': r.has_sell && rowNet(r) < 0 }">
              <span>{{ rowMidLabel(r) }}</span>
              <b class="num">{{ fmtMoney(rowMidValue(r)) }}</b>
            </div>
            <!-- 卖单：运费+手续费合计（卖出价减掉它就是到手价）
                 买单：有运费显示金额，没有就盖个「包邮」小章，不再显示 ¥0.00 -->
            <div class="rc-amt rc-amt-fee">
              <template v-if="r.has_sell">
                <span>卖运费+手续费</span>
                <b v-if="billPending(r)" class="fee-pending" title="闲鱼订单列表给的是商品金额；账单出来后再点「立即同步」，会按实收金额自动更正">待账单</b>
                <b v-else-if="feePending(r)" class="fee-pending" title="这一单没有服务费">无费用</b>
                <b v-else class="num">{{ fmtMoney(sellDeduct(r)) }}</b>
              </template>
              <template v-else>
                <span>买运费</span>
                <b v-if="hasShipFee(r.buy_shipping)" class="num">{{ fmtMoney(r.buy_shipping) }}</b>
                <span v-else class="ship-free">包邮</span>
              </template>
            </div>
          </div>
          <div class="rc-actions">
            <!-- 功能键放上面一排（图标在上、文字在下，五个等宽）；「查看详情」放下面整行，
                 避免它和功能键挤在一行把按钮压窄、也避免用户误点 -->
            <div class="action-icons">
              <button class="icon-btn" @touchstart.stop @click="filterByProduct(r)"
                      aria-label="看同商品" :title="'筛出「' + (r.product || '未命名商品') + '」的所有单据'">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.6-3.6"/></svg>
                <span class="icon-btn-t">同商品</span>
              </button>
              <button class="icon-btn icon-btn-discard" :class="{ 'icon-btn-discard-on': r.discarded_at }" :disabled="!!discarding"
                      @touchstart.stop @click="toggleDiscard(r)"
                      :aria-label="r.discarded_at ? '取消弃单' : '弃单'" :title="r.discarded_at ? '取消弃单' : '作废这一条（不占在库、不计成本）'">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2.5 11 13"/><path d="M21.5 2.5 15 21.5l-4-8.5-8.5-4z"/></svg>
                <span class="icon-btn-t">{{ r.discarded_at ? '取消弃单' : '弃单' }}</span>
              </button>
              <button class="icon-btn" :disabled="groupSettled(r) || !!r.discarded_at"
                      @touchstart.stop @click="openGroupPicker(r.id)"
                      :aria-label="r.discarded_at ? '已弃单，不能加入联单' : '加入联单'"
                      :title="r.discarded_at ? '已弃单，不能加入联单' : (groupSettled(r) ? '所在联单已完结，请先在「联单」页重新开启' : '加入联单')">
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg>
                <span class="icon-btn-t">联单</span>
              </button>
              <button class="icon-btn" :disabled="groupSettled(r)" @touchstart.stop @click="editRecord(r)" aria-label="编辑" :title="groupSettled(r) ? '所在联单已完结，不可编辑' : '编辑'">
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>
                <span class="icon-btn-t">编辑</span>
              </button>
              <button class="icon-btn icon-btn-danger" :disabled="groupSettled(r)" @touchstart.stop @click="del(r)" aria-label="删除" :title="groupSettled(r) ? '所在联单已完结，不可删除' : '删除'">
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/><path d="M10 11v6M14 11v6"/></svg>
                <span class="icon-btn-t">删除</span>
              </button>
            </div>
            <button class="action-primary action-primary-row" :class="{ open: expanded === r.id }" @touchstart.stop @click.stop="toggleDetail(r)">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
              {{ expanded === r.id ? '收起详情' : '查看详情' }}
            </button>
          </div>
          <div v-if="expanded === r.id" class="rc-detail">
            <!-- 卡片上的标题是单行截断的，这里给出完整标题，避免长名字看不全 -->
            <div class="rc-detail-full"><span>完整标题</span><b>{{ r.product || '未命名商品' }}</b></div>
            <!-- 成本优先显示：这是用户最关心的核算项，不该藏在编辑弹窗里。
                 手填的单件成本、联单分摊的成本、本条合计成本分三行写清楚。 -->
            <div><span>单件成本</span><b>{{ unitCostText(r) }}</b></div>
            <div><span>本条成本</span><b>{{ fmtMoney(r.cost || 0) }}</b></div>
            <!-- 只显示这条记录真有的那一侧：买单不列卖订单号/买家昵称，
                 卖单不列买订单号/卖家昵称，避免一屏「—」还容易看错边 -->
            <div v-if="r.has_buy"><span>买订单号</span><b class="mono">{{ r.buy_order_no || '—' }}</b></div>
            <div v-if="r.has_buy"><span>卖家昵称</span><b>{{ r.buy_counterparty || '—' }}</b></div>
            <div v-if="r.has_sell"><span>卖订单号</span><b class="mono">{{ r.sell_order_no || '—' }}</b></div>
            <div v-if="r.has_sell"><span>买家昵称</span><b>{{ r.sell_counterparty || '—' }}</b></div>
            <div v-if="r.has_sell"><span>软件服务费</span><b>{{ fmtMoney(r.sell_fee || 0) }}</b></div>
            <div v-if="r.has_sell"><span>无忧卖服务费</span><b>{{ fmtMoney(r.sell_wuyou_fee || 0) }}</b></div>
            <div><span>运费支出</span><b>{{ fmtMoney(r.buy_shipping + r.sell_shipping) }}</b></div>
            <div><span>运货标签</span><b>{{ [r.buy_ship_tag, r.sell_ship_tag].filter(Boolean).join(' / ') || '—' }}</b></div>
            <div><span>货物来源</span><b>{{ r.source || '未标注' }}</b></div>
            <div><span>归属</span><b>{{ r.owner || '—' }}</b></div>
            <div><span>备注</span><b>{{ r.note || '—' }}</b></div>
            <div><span>结算</span><b>{{ settleStateOf(r) }}</b></div>
            <div v-if="r.settled_at"><span>完结于</span><b>{{ r.settled_at }}</b></div>
            <div v-if="r.discarded_at"><span>弃单于</span><b>{{ r.discarded_at }}</b></div>

            <!-- 默认不展示截图，只给一个入口：想看的再点，列表也不会因此加载图片 -->
            <button v-if="r.attachment" type="button" class="rc-shot-btn"
                    @touchstart.stop @click.stop="openImage(r)">
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
                   stroke-linecap="round" stroke-linejoin="round">
                <rect x="3" y="4" width="18" height="16" rx="2.5" />
                <circle cx="8.5" cy="9.5" r="1.6" />
                <path d="M21 16l-5.2-5.2L5 21" />
              </svg>
              查看订单截图
            </button>
          </div><!-- /rc-detail -->
          </div><!-- /rec-card -->
        </div><!-- /rec-swipe -->
      </div><!-- /mobile-cards -->

      <ImageViewer v-if="viewerSrc" :src="viewerSrc" @close="viewerSrc = ''" />
      <GroupPicker v-if="groupPickerIds" :item-ids="groupPickerIds" :membership="groupMembership"
                   @close="groupPickerIds = null" @done="load"
                   @notify="(t, err) => emit('notify', t, err)" />

      <div v-if="!items.length && !loading" class="empty">
        <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg></div>
        <div class="empty-title">没有匹配的记录</div>
        <div class="empty-sub">换个关键词或重置筛选，也可以直接录入一笔</div>
        <div style="display:flex;gap:8px;justify-content:center;margin-top:16px;flex-wrap:wrap">
          <button class="btn btn-primary" @click="emit('add')">上传截图录入</button>
          <button class="btn btn-quiet" @click="emit('batch')">批量导入截图</button>
        </div>
      </div>
      <div v-else-if="loading && !items.length" class="skel-list" aria-label="正在加载">
        <div v-for="i in 4" :key="i" class="skel-row">
          <span class="skeleton skel-block"></span>
          <div class="skel-lines"><span class="skeleton skel-line w70"></span><span class="skeleton skel-line w40"></span></div>
        </div>
      </div>

      <div class="pager">
        <span class="pager-info">共 <b>{{ fmtNum(total) }}</b> 条 · 第 <b>{{ page }}</b> / {{ totalPages() }} 页</span>
        <div class="pager-controls">
          <select class="select page-size" v-model.number="pageSize">
            <option :value="10">10 条/页</option><option :value="20">20 条/页</option><option :value="50">50 条/页</option>
          </select>
          <div class="pager-btns">
            <button class="btn btn-sm btn-quiet" :disabled="page <= 1" @click="turnPage(-1)">上一页</button>
            <button class="btn btn-sm btn-quiet" :disabled="page >= totalPages()" @click="turnPage(1)">下一页</button>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
