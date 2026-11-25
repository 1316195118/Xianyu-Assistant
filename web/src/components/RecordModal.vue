<script setup>
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { api } from '../api'
import { localRecognize, releaseOCR } from '../ocr'
import { fmtMoney, fmtMoneyPlain } from '../utils'
import { activeShop } from '../shopStore'
import ImageViewer from './ImageViewer.vue'
import HelpTip from './HelpTip.vue'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => emit('close'))


const props = defineProps({
  record: { type: Object, default: null },   // 传入即为编辑模式
  viewOnly: { type: Boolean, default: false },// true=只读查看（联单里点订单进来就是这个）
  prefill: { type: Object, default: null },   // 定价速算带过来的预填值（仅新增时生效）
  initialFile: { type: Object, default: null } // PWA 分享 / 粘贴截图直接进入识别
})
const emit = defineEmits(['close', 'saved', 'open-settings'])

const isEdit = computed(() => !!props.record)

// 只读查看：一开始不可编辑，点「编辑」才切成可改。
// 这样从联单里点订单进来不会手一滑就改坏数据。
const readonly = ref(false)
watch(() => props.viewOnly, (v) => { readonly.value = !!v }, { immediate: true })
function startEdit() {
  readonly.value = false
  errMsg.value = ''
}
// 多店铺下最容易出的错是「在错的店里录单」。标题直接标出归属店铺，
// 录入前扫一眼就能发现，比事后翻店铺对账便宜得多。
// 时间字段：库里存 "YYYY-MM-DD HH:MM:SS"，datetime-local 需要 "YYYY-MM-DDTHH:MM(:SS)"。
// 只做格式搬运不改精度；识别有时只还原到日期，按当天 00:00:00 处理，与原先的纯文本行为一致。
function toLocalInput(v) {
  const s = String(v || '').trim()
  if (!s) return ''
  const full = s.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2})(:\d{2})?/)
  if (full) return `${full[1]}T${full[2]}${full[3] || ':00'}`
  const dayOnly = s.match(/^(\d{4}-\d{2}-\d{2})$/)
  if (dayOnly) return `${dayOnly[1]}T00:00:00`
  return ''
}
function fromLocalInput(v) {
  const s = String(v || '').trim()
  if (!s) return ''
  const t = s.replace('T', ' ')
  return t.length === 16 ? t + ':00' : t
}
const buyTimeLocal = computed({
  get: () => toLocalInput(form.buy_time),
  set: (v) => { form.buy_time = fromLocalInput(v) }
})
const sellTimeLocal = computed({
  get: () => toLocalInput(form.sell_time),
  set: (v) => { form.sell_time = fromLocalInput(v) }
})

const shopName = computed(() => activeShop.value?.name || '我的闲鱼')
const shopColor = computed(() => activeShop.value?.color || '#f5b400')
const DRAFT_KEY = 'xy_record_draft'

// 编辑模式：按记录类型决定显示哪一侧字段——买单/独立单显示买进信息，卖单/独立单显示卖出信息。
// 避免“编辑买单时出现卖单内容、编辑卖单时出现买单内容”。新增模式仍由 entrySide 控制。
const editShowBuy = computed(() => {
  if (!isEdit.value) return true
  const k = props.record?.kind
  return k === 'purchase' || k === 'standalone'
})
const editShowSell = computed(() => {
  if (!isEdit.value) return true
  const k = props.record?.kind
  return k === 'sale' || k === 'standalone'
})

const blank = () => ({
  product: '',
  buy_price: 0, buy_shipping: 0, buy_ship_tag: '', buy_order_no: '', buy_time: '', buy_counterparty: '',
  sell_price: 0, sell_fee: 0, sell_wuyou_fee: 0, sell_net: 0, sell_shipping: 0, sell_ship_tag: '',
  sell_order_no: '', sell_time: '', sell_counterparty: '',
  note: '',
  source: '',
  owner: '',
  quantity: 1, parent_id: 0, unit_cost: 0,
  cost_free: false
})
const form = reactive(blank())

// ---------- 归属人（寄卖人 / 代采委托人）候选：输入单字自动匹配，点输入框看全部 ----------
// 寄卖 / 代采会按来源调整成本口径；其它来源也允许填写归属，切换来源时不能把用户刚填的人名清掉。
const isExternalSource = (v) => v === '寄卖' || v === '寄售' || v === '代采' || v === '代买'
watch(() => form.source, (v) => {
  if (hydrating) return
  if (v === '寄卖' || v === '寄售') {
    // 寄卖是别人的货：默认无成本，用户之后仍可手动改回来
    form.cost_free = true
    form.unit_cost = 0
  } else if (v === '代采' || v === '代买') {
    // 代采是我先垫钱买：成本要如实填（后面按垫付金额向归属人收），只保留归属
    form.cost_free = false
  } else {
    // 其它来源只撤掉自动加的「无成本」，归属是用户手动填写的业务数据，必须保留。
    form.cost_free = false
  }
})


const ownerAll = ref([])
const ownerOpen = ref(false)
const ownerCandidates = computed(() => {
  const q = (form.owner || '').trim()
  return ownerAll.value.filter((n) => !q || n.includes(q))
})
async function loadOwners() {
  try {
    const names = (await api.consignOwners()).owners || []
    ownerAll.value = [...new Set(names.map((n) => String(n || '').trim()).filter(Boolean))]
      .sort((a, b) => a.localeCompare(b, 'zh-CN'))
  } catch (e) { /* 忽略 */ }
}
function pickOwner(n) {
  const name = String(n || '').trim()
  if (!name) return
  form.owner = name
  if (!ownerAll.value.includes(name)) {
    ownerAll.value = [...ownerAll.value, name].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  }
  ownerOpen.value = false
}
function ownerBlur() { setTimeout(() => { ownerOpen.value = false }, 130) }

// ---------- 同商品历史成交价：输入商品名后给定价参考 ----------
const priceHint = ref(null)      // { count, last, avg, max, min }
let hintTimer = 0
let hintSeq = 0                  // 防止旧请求覆盖新结果
watch(() => form.product, (name) => {
  clearTimeout(hintTimer)
  const v = (name || '').trim()
  if (!v || readonly.value || props.viewOnly) { priceHint.value = null; return }
  hintTimer = setTimeout(async () => {
    const seq = ++hintSeq
    try {
      const r = await api.priceHint(v)
      if (seq !== hintSeq) return   // 名字已经变了，丢弃过期结果
      priceHint.value = (r && r.count > 0) ? r : null
    } catch (e) {
      if (seq === hintSeq) priceHint.value = null
    }
  }, 550)
})

// ---------- 运费标签：跟着运费金额自动判定 ----------
// 规则（对账口径：只有 >0.1 才算「有运费」，避免 0.01 元这类脏数据误判）：
//   买单：无运费 → 包邮，有运费 → 到付
//   卖单：有运费 → 卖家承担，无运费 → 买家承担
// 手动改过标签就以手动为准，直到金额本身再变一次，再回到自动。
const SHIP_EPS = 0.1
const hasShip = (v) => (Number(v) || 0) > SHIP_EPS
const autoBuyTag = (v) => (hasShip(v) ? '到付' : '包邮')
const autoSellTag = (v) => (hasShip(v) ? '卖家承担' : '买家承担')
// 载入已有记录时不要覆盖用户当初存下的标签
let hydrating = false
watch(() => form.buy_shipping, (v) => {
  if (hydrating || form.buy_ship_tag === '自提') return
  const t = autoBuyTag(v)
  if (form.buy_ship_tag !== t) form.buy_ship_tag = t
})
watch(() => form.sell_shipping, (v) => {
  if (hydrating || form.sell_ship_tag === '自提') return
  const t = autoSellTag(v)
  if (form.sell_ship_tag !== t) form.sell_ship_tag = t
})

// 本次录入侧：buy / sell / ''（两侧手动填）。编辑时固定为 ''
const entrySide = ref('')
const file = ref(null)
const previewUrl = ref('')
const recState = ref('idle')  // idle | recognizing | done | error
const recMsg = ref('')
const saving = ref(false)
const errMsg = ref('')
// 报错时抖一下：保存按钮回到「保存修改」看不出失败，必须有个动作让人知道
const shaking = ref(false)
let shakeTimer = null
watch(errMsg, (v) => {
  if (!v) return
  shaking.value = false
  clearTimeout(shakeTimer)
  requestAnimationFrame(() => {
    shaking.value = true
    shakeTimer = setTimeout(() => { shaking.value = false }, 560)
  })
})
const mergedHint = ref('')
const engine = ref('local')   // local | vision
const engineName = computed(() => engine.value === 'local' ? '本地 OCR' : '视觉大模型')
const ocrRaw = ref('')
const showRaw = ref(false)
const recProgress = ref(0)
const allowMerge = ref(true) // 卖出单是否自动关联同名买进单
// 识别诊断：显示构建版本 + 每个关键字段是否填到表单，便于一眼定位「没识别到」的原因
const buildVersion = computed(() => {
  try { return document.querySelector('meta[name="xy-build"]')?.content || 'dev' } catch (e) { return 'dev' }
})
// 卖出时可多选买进单建立联动单（N:M）
const selectedPurchases = ref([])
const purchaseQuery = ref('')
const purchasePickerOpen = ref(false)
const purchaseCandidates = computed(() => {
  const key = purchaseQuery.value.trim().toLowerCase()
  const pool = purchasePool.value.filter(p => !isSettledBuy(p.id)) // 隐藏已完结联单的买单
  let list = pool
  if (key) {
    list = pool.filter(p => (p.product + ' ' + (p.buy_order_no || '') + ' ' + (p.buy_counterparty || '')).toLowerCase().includes(key))
  } else if (form.product.trim()) {
    // 按标题做相似度推荐：同名/包含关系优先
    const nk = normProductLocal(form.product)
    list = [...pool].sort((a, b) => {
      const sa = normProductLocal(a.product).includes(nk) || nk.includes(normProductLocal(a.product)) ? 100 : 0
      const sb = normProductLocal(b.product).includes(nk) || nk.includes(normProductLocal(b.product)) ? 100 : 0
      return sb - sa
    })
  }
  // 已选的置顶
  return [...list].sort((a, b) => (selectedPurchases.value.includes(b.id) ? 1 : 0) - (selectedPurchases.value.includes(a.id) ? 1 : 0)).slice(0, 30)
})
function togglePurchase(id) {
  const i = selectedPurchases.value.indexOf(id)
  if (i >= 0) selectedPurchases.value.splice(i, 1)
  else selectedPurchases.value.push(id)
}
function autoSelectSimilar() {
  const nk = normProductLocal(form.product)
  let n = 0
  for (const p of purchasePool.value) {
    if (isSettledBuy(p.id)) continue // 已完结联单的买单不可选
    const np = normProductLocal(p.product)
    if (nk && (np === nk || np.includes(nk) || nk.includes(np)) && !selectedPurchases.value.includes(p.id)) {
      selectedPurchases.value.push(p.id); n++
    }
  }
  // 始终打开买进单列表：哪怕没有自动匹配，也能手动选择；不再“点了没反应”
  purchasePickerOpen.value = true
  return n
}
const recDiag = ref(null)
// 诊断条字段：用 SVG 勾/叉替代文本符号，保持全站图标风格一致
const diagFields = [
  { key: 'order', label: '订单号' },
  { key: 'time', label: '时间' },
  { key: 'cp', label: '昵称' },
  { key: 'product', label: '商品' },
  { key: 'price', label: '价格' }
]
const CK = 'M5 12l4.5 4.5L19 7'      // 勾
const XK = 'M6 6l12 12M18 6L6 18'    // 叉
// 金额疑似被遮挡/识别不全（后端 detect 到关键词后紧接 * @ 等杂符）的提醒，避免错金额被静默保存
const recWarn = ref('')
const salvagedKeys = ref([])
const slicedDone = ref(false)
const timeApprox = ref(false)  // 时间仅由支付宝交易号推断出日期（时分秒为 00:00:00）

// 1:N 匹配（卖出单 → 买进单）
const matchedParent = ref(null)     // 已关联的买进单
// 联单分摊的单件成本：卖单自己没填成本、但所在联单有买进单时由后端算出。
// 只用于预览与提示，不写回 form.unit_cost（否则一保存就把动态分摊固化成手填值）。
const allocCost = ref(0)
const matchCandidates = ref([])     // 相似候选买进单
const showMatchPicker = ref(false)  // 是否弹出候选选择
const matchLoading = ref(false)

// 运费标签
const tags = ref([])
const customBuy = ref(false)
const customSell = ref(false)
const newTag = ref('')
const tagScope = ref('')  // buy | sell

async function loadTags() {
  try { tags.value = await api.getTags() } catch (e) { /* ignore */ }
}
// 预加载买进单索引（后端内存索引，一次返回），弹窗打开即可秒选，无需等匹配请求
const purchasePool = ref([])
const purchaseLoading = ref(false)
const purchaseError = ref('')
const groupsMetaError = ref('')
const linkboxError = computed(() => purchaseError.value || groupsMetaError.value)
async function loadPurchases() {
  purchaseLoading.value = true; purchaseError.value = ''
  try { purchasePool.value = (await api.purchases()).items || [] }
  catch (e) { purchasePool.value = []; purchaseError.value = e.message || '买进单加载失败' }
  finally { purchaseLoading.value = false }
}
// 已完结联单里的买单 id 集合：创建卖单时这些买单不可再被选入，
// 避免把新卖单追加到“已完结”的联单里（即“选择联单”处隐藏已完结联单）。
const settledItemIds = ref(new Set())
async function loadGroupsMeta() {
  groupsMetaError.value = ''
  try {
    const gs = (await api.groups()).items || []
    const set = new Set()
    for (const g of gs) if (g.settled_at) for (const id of (g.item_ids || [])) set.add(id)
    settledItemIds.value = set
  } catch (e) {
    // 拿不到「哪些买单已完结」时，宁可空集合也别悄悄放行：
    // 但必须在界面上说明，否则用户会选中一个其实不能动的买单
    settledItemIds.value = new Set()
    groupsMetaError.value = '完结联单状态加载失败'
  }
}
function retryLinkbox() { loadPurchases(); loadGroupsMeta() }
const isSettledBuy = (id) => settledItemIds.value.has(id)
function normProductLocal(s) {
  return String(s || '').toLowerCase().replace(/[\s，,。、:：\-_/()（）]/g, '')
}
async function loadEngine() {
  try { engine.value = (await api.getSettings()).ocr_engine || 'local' } catch (e) { /* 默认本地 */ }
}
onMounted(() => {
  loadTags(); loadEngine(); loadPurchases(); loadGroupsMeta(); loadOwners()
  if (!isEdit.value && props.initialFile instanceof Blob) {
    nextTick(() => setFile(props.initialFile))
  }
})

// 新增模式下草稿自动存到本机，误关弹窗不会丢已填内容
const feedbackMsg = ref('')
const attachmentName = ref('')
const viewerSrc = ref('')   // 全屏查看附件
watch(form, () => {
  if (isEdit.value) return
  try { localStorage.setItem(DRAFT_KEY, JSON.stringify(form)) } catch (e) {}
}, { deep: true })
async function sendFeedback(correct) {
  try {
    await api.ocrFeedback({ record_id: props.record?.id || 0, correct, note: form.product })
    feedbackMsg.value = correct ? '已记录，谢谢反馈' : '已记录，请手动核对字段'
  } catch (e) { feedbackMsg.value = '反馈提交失败' }
}
async function onAttachment(e) {
  const f = e.target.files?.[0]
  if (!f || !isEdit.value) return
  try {
    const r = await api.uploadAttachment(props.record.id, f)
    attachmentName.value = r.name || f.name
    e.target.value = ''
  } catch (err) { errMsg.value = '附件上传失败：' + err.message }
}

// 窗口焦点管理：打开时把焦点移入弹窗容器（不直接聚焦输入框，避免移动端弹出键盘），
// Tab 限制在弹窗内循环，关闭后把焦点还给触发按钮
const maskEl = ref(null)
let lastFocus = null
onMounted(async () => {
  lastFocus = document.activeElement
  await nextTick()
  maskEl.value?.focus?.()
})
onUnmounted(() => {
  try { lastFocus?.focus?.() } catch (e) { /* ignore */ }
  // 本地 OCR 的 worker 和语言数据占用明显，关闭录入弹窗后释放；
  // 下次识别会按需重新初始化，不会影响已填入的识别结果。
  releaseOCR()
})
function onModalKey(e) {
  if (e.key !== 'Tab' || !maskEl.value) return
  const root = maskEl.value
  const els = [...root.querySelectorAll('button:not([disabled]), input:not([disabled]):not([hidden]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])')]
    .filter(el => el.offsetParent !== null)
  if (!els.length) return
  const first = els[0], last = els[els.length - 1]
  if (!root.contains(document.activeElement)) { e.preventDefault(); first.focus() }
  else if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
}

// 乐观锁基线：编辑期间若别人也改了同一条，保存时会拿到 409，
// 需要把基线更新为服务端最新版本，否则用户重试会一直冲突。
const expectedUpdatedAt = ref('')

watch(() => props.record, (r) => {
  expectedUpdatedAt.value = r?.updated_at || ''
  allocCost.value = Number(r?.alloc_cost) || 0
  // 回填期间关掉「运费标签自动判定」，否则会把这条记录当初存的标签改掉
  hydrating = true
  Object.assign(form, blank(), r ? {
    product: r.product,
    buy_price: r.buy_price, buy_shipping: r.buy_shipping, buy_ship_tag: r.buy_ship_tag,
    buy_order_no: r.buy_order_no, buy_time: r.buy_time, buy_counterparty: r.buy_counterparty,
    sell_price: r.sell_price, sell_fee: r.sell_fee, sell_wuyou_fee: r.sell_wuyou_fee || 0, sell_net: r.sell_net,
    sell_shipping: r.sell_shipping, sell_ship_tag: r.sell_ship_tag,
    sell_order_no: r.sell_order_no, sell_time: r.sell_time, sell_counterparty: r.sell_counterparty,
    note: r.note, source: r.source || '', owner: r.owner || '', quantity: r.quantity || 1, parent_id: r.parent_id || 0, unit_cost: r.unit_cost || 0,
    cost_free: !!r.cost_free
  } : {})
  if (r && r.parent_id) {
    matchedParent.value = { id: r.parent_id, product: r.product, buy_counterparty: r.buy_counterparty, buy_price: r.buy_price, child_count: r.child_count || 0 }
  } else {
    matchedParent.value = null
  }
  entrySide.value = ''
  file.value = null; previewUrl.value = ''
  recState.value = 'idle'; recMsg.value = ''; errMsg.value = ''; mergedHint.value = ''
  ocrRaw.value = ''; showRaw.value = false; recProgress.value = 0; recWarn.value = ''
  selectedPurchases.value = []; purchaseQuery.value = ''; purchasePickerOpen.value = false
  attachmentName.value = r?.attachment || ''
  feedbackMsg.value = ''
  if (!r) {
    try {
      const d = JSON.parse(localStorage.getItem(DRAFT_KEY) || 'null')
      if (d && typeof d === 'object') Object.assign(form, d)
    } catch (e) {}
    // 新记录：先按金额给出默认标签（无运费=包邮 / 买家承担）
    form.buy_ship_tag = form.buy_ship_tag || autoBuyTag(form.buy_shipping)
    form.sell_ship_tag = form.sell_ship_tag || autoSellTag(form.sell_shipping)
    // 定价速算带过来的值最后覆盖，且在卖出侧：默认落「两侧一起填」，
    // 这里直接切到卖出侧，避免用户以为填的是买进侧。
    if (props.prefill && typeof props.prefill === 'object') {
      Object.assign(form, props.prefill)
      entrySide.value = 'sell'
      form.sell_ship_tag = autoSellTag(form.sell_shipping)
      form.sell_net = Math.round((Number(form.sell_price) - Number(form.sell_shipping) - Number(form.sell_fee) - Number(form.sell_wuyou_fee)) * 100) / 100
    }
  }
  hydrating = false
}, { immediate: true })

function onFile(e) {
  const f = e.target.files?.[0]
  if (f) setFile(f)
}
function onDrop(e) {
  const f = e.dataTransfer.files?.[0]
  if (f) setFile(f)
}
function setFile(f) {
  if (!f.type.startsWith('image/')) { recMsg.value = '请选择图片文件'; recState.value = 'error'; return }
  file.value = f
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = URL.createObjectURL(f)
  // 换图 = 一张全新的截图，表单从干净状态开始，杜绝上一次识别的残留
  resetFormForNewImage()
  doRecognize()
}
function clearFile() {
  file.value = null
  if (previewUrl.value) URL.revokeObjectURL(previewUrl.value)
  previewUrl.value = ''
  recState.value = 'idle'; recMsg.value = ''; mergedHint.value = ''; recWarn.value = ''
}

// 识别类字段（OCR 会覆盖的）；运费/运费标签/备注属于手填，重置时保留
function clearRecognizedFields() {
  form.buy_price = 0
  form.buy_order_no = ''
  form.buy_counterparty = ''
  form.buy_time = ''
  form.sell_price = 0
  form.sell_fee = 0
  form.sell_wuyou_fee = 0
  form.sell_net = 0
  form.sell_order_no = ''
  form.sell_counterparty = ''
  form.sell_time = ''
}

function fillFields(d) {
  const side = d.side === 'buy' ? 'buy' : 'sell'
  entrySide.value = side
  // 关键：一次识别对应一张新截图，先清掉两侧旧的识别结果，
  // 避免「先传卖单、再换买进单」时另一侧残留上一张截图的数据
  clearRecognizedFields()
  form.product = d.product || form.product
  if (side === 'buy') {
    form.buy_price = Number(d.price) || 0
    form.buy_shipping = Number(d.shipping) || 0
    form.buy_order_no = d.order_no || ''
    form.buy_counterparty = d.counterparty || ''
    form.buy_time = d.deal_time || ''
    // 运费标签由上面的 watch(form.buy_shipping) 自动判定（>0.1=到付，否则包邮）
  } else {
    form.sell_price = Number(d.price) || 0
    form.sell_fee = Number(d.fee) || 0
    form.sell_wuyou_fee = Number(d.wuyou_fee) || 0
    form.sell_net = Number(d.net_price) || 0
    form.sell_shipping = Number(d.shipping) || 0
    form.sell_order_no = d.order_no || ''
    form.sell_counterparty = d.counterparty || ''
    form.sell_time = d.deal_time || ''
    // 运费标签同样由 watch(form.sell_shipping) 判定（>0.1=卖家承担，否则买家承担）
  }
  return side
}

// 换一张截图：整体重置表单（含手填运费/备注），从干净状态开始
function resetFormForNewImage() {
  Object.assign(form, blank())
  entrySide.value = ''
  recState.value = 'idle'; recMsg.value = ''; mergedHint.value = ''; recWarn.value = ''
  ocrRaw.value = ''; showRaw.value = false; recProgress.value = 0
  errMsg.value = ''
  customBuy.value = false; customSell.value = false; newTag.value = ''
  allowMerge.value = true
  clearMatch()
}

// ---------- 1:N 匹配：卖出单关联到买进单 ----------
function clearMatch() {
  matchedParent.value = null
  matchCandidates.value = []
  showMatchPicker.value = false
  form.parent_id = 0
  form.unit_cost = 0
}

function applyMatch(parent) {
  matchedParent.value = parent
  form.parent_id = parent.id
  const q = Number(parent.quantity) || 1
  form.unit_cost = Math.round(((Number(parent.buy_price) || 0) + (Number(parent.buy_shipping) || 0)) * 100 / q) / 100
  mergedHint.value = `已关联买进单「${parent.product}」（卖家 ${parent.buy_counterparty || '—'}，买进价 ${fmtMoneyPlain(parent.buy_price)}，已挂 ${parent.child_count || 0} 笔）`
}

async function runMatch() {
  if (entrySide.value !== 'sell' || !allowMerge.value || !form.product.trim()) {
    clearMatch()
    return
  }
  // 1) 优先用预加载的买进单索引做本地精确匹配（零网络等待，秒开）
  if (purchasePool.value.length) {
    const key = normProductLocal(form.product.trim())
    const hit = purchasePool.value.find(p => p.norm === key || normProductLocal(p.product) === key)
    // 命中的买进单若属于已完结联单，则不作为关联目标（避免往已完结联单追加记录）
    if (hit && !isSettledBuy(hit.id)) {
      applyMatch(hit)
      showMatchPicker.value = false
      return
    }
  }
  // 2) 未精确命中才请求后端做相似匹配（后端内存索引，也已很快）
  matchLoading.value = true
  try {
    const r = await api.match({ product: form.product.trim(), side: 'sell' })
    if (r.exact && r.match && r.match.id && !isSettledBuy(r.match.id)) {
      applyMatch(r.match)
      showMatchPicker.value = false
    } else if (r.candidates && r.candidates.length) {
      const f = r.candidates.filter(c => !isSettledBuy(c.id)) // 隐藏已完结联单的买单
      if (f.length) {
        matchCandidates.value = f
        showMatchPicker.value = true
      } else {
        clearMatch()
      }
    } else {
      clearMatch()
    }
  } catch (e) {
    clearMatch()
  } finally {
    matchLoading.value = false
  }
}

function chooseCandidate(id) {
  const p = matchCandidates.value.find(c => c.id === id)
  if (p) applyMatch(p)
  showMatchPicker.value = false
}
function skipMatch() {
  clearMatch()
  mergedHint.value = '未关联买进单，本单作为独立卖出记录保存'
}

async function doRecognize(forceEngine) {
  if (!file.value) return
  const eng = forceEngine || engine.value
  recState.value = 'recognizing'
  recMsg.value = `${eng === 'local' ? '本地 OCR' : '视觉大模型'}识别中…`
  errMsg.value = ''; mergedHint.value = ''; ocrRaw.value = ''; recProgress.value = 0
  recDiag.value = null; salvagedKeys.value = []; slicedDone.value = false; timeApprox.value = false
  selectedPurchases.value = []; purchaseQuery.value = ''; purchasePickerOpen.value = false
  try {
    let d
    if (eng === 'local') {
      const r = await localRecognize(file.value, (p, label) => {
        if (p >= 0) { recProgress.value = p; recMsg.value = `本地 OCR 识别中 ${p}%` }
        else if (label) recMsg.value = label
      })
      d = r.fields
      ocrRaw.value = r.rawText
      salvagedKeys.value = r.salvaged || []
      slicedDone.value = !!r.sliced
    } else {
      d = await api.recognize(file.value)
    }
    const side = fillFields(d)
    recWarn.value = d.amount_garbled
      ? '金额疑似被遮挡或识别不全（OCR 把数字读成了 * 等杂符），成交价 / 到手价可能不准，请务必手动核对后再保存。'
      : ''
    timeApprox.value = !!d.deal_time_approx
    recState.value = 'done'
    recMsg.value = `${eng === 'local' ? '本地 OCR' : '视觉大模型'}已识别为${side === 'buy' ? '买进' : '卖出'}单，字段已填入对应区域`
    // 诊断以「表单里最终有没有值」为准，而不是后端返回了什么
    recDiag.value = {
      order: !!(form.buy_order_no || form.sell_order_no),
      time: !!(form.buy_time || form.sell_time),
      cp: !!(form.buy_counterparty || form.sell_counterparty),
      product: !!form.product,
      price: Number(side === 'buy' ? form.buy_price : form.sell_price) > 0,
      rawLen: ocrRaw.value.length,
      salvaged: salvagedKeys.value.length,
      sliced: slicedDone.value
    }
    if (!recDiag.value.order || !recDiag.value.time || !recDiag.value.cp) showRaw.value = true
    if (side === 'buy') {
      mergedHint.value = `保存后将新建一条买进单；日后卖出此商品时再识别会自动关联到它`
    } else {
      mergedHint.value = `正在按商品名匹配可关联的买进单…`
      await runMatch()
    }
  } catch (e) {
    recState.value = 'error'
    recMsg.value = e.message || '识别失败'
  }
}

async function switchEngineAndRetry() {
  const next = engine.value === 'local' ? 'vision' : 'local'
  await doRecognize(next)
}

// 切换录入侧时，清掉另一侧残留的识别结果，避免保存时把无关金额写进记录
function switchEntrySide(side) {
  if (entrySide.value === side) return
  if (side === 'buy') {
    form.sell_price = 0; form.sell_fee = 0; form.sell_wuyou_fee = 0; form.sell_net = 0
    form.sell_order_no = ''; form.sell_counterparty = ''; form.sell_time = ''
    form.sell_shipping = 0; form.sell_ship_tag = ''
    clearMatch()
    form.buy_ship_tag = autoBuyTag(form.buy_shipping)
  } else if (side === 'sell') {
    form.buy_price = 0
    form.buy_order_no = ''; form.buy_counterparty = ''; form.buy_time = ''
    form.buy_shipping = 0; form.buy_ship_tag = ''
    clearMatch()
    if (form.product.trim()) runMatch()
  } else {
    clearMatch()
  }
  entrySide.value = side
}

function autoNet() {
  form.sell_net = Math.max(0, Math.round(((Number(form.sell_price) || 0) - (Number(form.sell_fee) || 0) - (Number(form.sell_wuyou_fee) || 0)) * 100) / 100)
}

// 标签选择
function onTagSelect(scope, ev) {
  const v = ev.target.value
  if (v === '__new__') {
    if (scope === 'buy') { customBuy.value = true; form.buy_ship_tag = '' }
    else { customSell.value = true; form.sell_ship_tag = '' }
    tagScope.value = scope
    newTag.value = ''
  } else {
    // 手动选的标签会一直保留，直到运费金额再变一次（金额才是唯一判定依据）
    if (scope === 'buy') {
      customBuy.value = false; form.buy_ship_tag = v
      if (v === '自提') form.buy_shipping = 0
    } else {
      customSell.value = false; form.sell_ship_tag = v
      if (v === '自提') { form.sell_shipping = 0; recalcNet() }
    }
  }
}
async function saveNewTag() {
  const name = newTag.value.trim()
  if (!name) return
  try {
    await api.addTag(name)
    await loadTags()
    if (tagScope.value === 'buy') {
      form.buy_ship_tag = name; customBuy.value = false
      if (name === '自提') form.buy_shipping = 0
    } else {
      form.sell_ship_tag = name; customSell.value = false
      if (name === '自提') { form.sell_shipping = 0; recalcNet() }
    }
    newTag.value = ''
  } catch (e) {
    errMsg.value = e.message
  }
}

// 字段级校验：逐字段标红并给出文案，避免只在底部汇总一个错误
const errors = ref({})
const collapsed = reactive({ buy: false, sell: false })
function clearErr(k) {
  if (!errors.value[k]) return
  const next = { ...errors.value }
  delete next[k]
  errors.value = next
}
function validate() {
  const e = {}
  if (!form.product.trim()) e.product = '请填写商品名称（用于同名自动配对）'
  const moneyFields = ['buy_price', 'buy_shipping', 'sell_price', 'sell_fee', 'sell_wuyou_fee', 'sell_net', 'sell_shipping']
  for (const k of moneyFields) if (Number(form[k]) < 0) e[k] = '金额不能为负数'
  if (Number(form.quantity) < 1) e.quantity = '件数至少为 1'
  const hasAmount = Number(form.buy_price) > 0 || Number(form.sell_net) > 0 ||
    Number(form.sell_price) > 0 || !!form.buy_order_no || !!form.sell_order_no
  if (!hasAmount) e.buy_price = '请至少填写买进价或卖出金额（或订单号）'
  errors.value = e
  if (Object.keys(e).length) errMsg.value = `还有 ${Object.keys(e).length} 处需要检查，已标红`
  return !Object.keys(e).length
}

const hasSell = computed(() => Number(form.sell_net) > 0 || Number(form.sell_price) > 0 || !!form.sell_order_no)
const externalGoods = computed(() => isExternalSource(form.source))
// 本条利润：与保存后的口径保持一致——
// 收入取「成交价」，没填成交价才用「到手价 + 服务费」反推；
// 成本 = 买价 + 买运费 + 卖运费 + 软件服务费 + 无忧卖（关联买进单时买价换成单件成本×件数）。
// 之前只按「到手价」算，用户只填成交价、没填到手价时预览会显示成大额亏损，保存后却是有利润。
const feeAll = computed(() => (Number(form.sell_fee) || 0) + (Number(form.sell_wuyou_fee) || 0))
const revenue = computed(() => {
  const sp = Number(form.sell_price) || 0
  if (sp > 0) return sp
  return (Number(form.sell_net) || 0) + feeAll.value
})
// 到手价 = 成交价 − 卖运费 − 软件服务费 − 无忧卖。
// 只在用户改动这几个金额时重算：载入历史记录不动原值（可能有平台补贴/优惠，
// 与公式不一致是正常的），改完价后自动跟着变，不用手算。
function recalcNet() {
  const p = Number(form.sell_price) || 0
  if (p <= 0) return
  const v = p - (Number(form.sell_shipping) || 0) - (Number(form.sell_fee) || 0) - (Number(form.sell_wuyou_fee) || 0)
  form.sell_net = Math.round(v * 100) / 100
}
// 勾了「无成本」就没必要再填单件成本，直接清零，避免两个口径打架
watch(() => form.cost_free, (v) => { if (v) form.unit_cost = 0 })
const profit = computed(() => {
  const sellShip = Number(form.sell_shipping) || 0
  // 单件成本优先用手填值；没填但联单有买进单时，用后端算的分摊值预览，
  // 否则弹窗里的利润会比首页/排行高出一整笔进货成本。
  const unit = Number(form.unit_cost) || Number(allocCost.value) || 0
  const byUnit = unit > 0
  const goods = byUnit ? unit * (Number(form.quantity) || 1) : (Number(form.buy_price) || 0)
  const goodsShip = byUnit ? 0 : (Number(form.buy_shipping) || 0)
  return Math.round((revenue.value - goods - goodsShip - sellShip - feeAll.value) * 100) / 100
})
const holding = computed(() => (Number(form.buy_price) || 0) + (Number(form.buy_shipping) || 0))

const justSaved = ref(false)
async function save() {
  errMsg.value = ''
  if (saving.value || justSaved.value) return
  // 已完结联单里的记录：先本地拦下，立刻给提示，不用等接口 409
  if (isEdit.value && props.record?.id && settledItemIds.value.has(props.record.id)) {
    errMsg.value = '这条记录在已完结的联单里，现在不能改。请先到「联单」页把那张联单点「重新开启」，再回来保存。'
    return
  }
  if ((form.source === '寄卖' || form.source === '寄售') && !form.owner.trim()) {
    errMsg.value = '寄卖单必须填写归属人（这单货是谁的）'
    return
  }
  if ((form.source === '代采' || form.source === '代买') && !form.owner.trim()) {
    errMsg.value = '代采单必须填写归属人（这是替谁买的）'
    return
  }
  // 代采的应付金额 = 买进价 + 买入运费，两个都空就没有金额可以找归属人收
  if ((form.source === '代采' || form.source === '代买') && !(Number(form.buy_price) > 0) && !(Number(form.buy_shipping) > 0)) {
    errMsg.value = '代采单要填买进价（垫了多少钱），不然没金额可以找归属人收'
    return
  }
  if (!validate()) return
  saving.value = true
  // 服务费兜底：识别漏抓手续费行但成交价>到手价时，用差额反推总服务费。
  // 到手价已经扣过卖运费，所以先减运费，避免把运费重复记成服务费。
  const sp = Number(form.sell_price) || 0
  const sn = Number(form.sell_net) || 0
  let sf = Number(form.sell_fee) || 0
  const sw = Number(form.sell_wuyou_fee) || 0
  if (entrySide.value === 'sell' && sf === 0 && sw === 0 && sn > 0 && sp > sn) {
    sf = Math.max(0, Math.round((sp - sn - (Number(form.sell_shipping) || 0)) * 100) / 100)
  }
  const payload = {
    product: form.product.trim(),
    buy_price: Number(form.buy_price) || 0, buy_shipping: Number(form.buy_shipping) || 0,
    buy_ship_tag: form.buy_ship_tag, buy_order_no: form.buy_order_no.trim(),
    buy_time: form.buy_time.trim(), buy_counterparty: form.buy_counterparty.trim(),
    sell_price: sp, sell_fee: sf,
    sell_wuyou_fee: sw,
    sell_net: sn, sell_shipping: Number(form.sell_shipping) || 0,
    sell_ship_tag: form.sell_ship_tag, sell_order_no: form.sell_order_no.trim(),
    sell_time: form.sell_time.trim(), sell_counterparty: form.sell_counterparty.trim(),
    note: form.note.trim(),
    source: form.source || '',
    owner: (form.owner || '').trim(),
    quantity: Number(form.quantity) || 1,
    parent_id: Number(form.parent_id) || 0,
    unit_cost: Number(form.unit_cost) || 0,
    cost_free: !!form.cost_free
  }
  if (isEdit.value) payload.expected_updated_at = expectedUpdatedAt.value
  try {
    let saleId = props.record?.id || 0
    let autoGroupId = 0
    const incomplete = []
    if (isEdit.value) {
      await api.update(props.record.id, payload)
    } else {
      payload.side = entrySide.value
      // 不想自动合并时，让后端跳过配对、直接新建一条独立记录
      if (!allowMerge.value) payload.no_merge = true
      const res = await api.create(payload)
      saleId = res.item?.id || res.id || 0
      // 两侧一起填（side 为空）时后端会自动建联单，把 id 带回去让提示更明确
      autoGroupId = res.group_id || 0
    }
    // 新增记录后，把识别用的截图自动存为附件。
    // 本地 OCR 全程在浏览器里跑、原图从未上传，不在这里抓一次就永远丢了。
    if (!isEdit.value && saleId && file.value) {
      try {
        await api.uploadAttachment(saleId, file.value)
      } catch {
        incomplete.push('截图上传')
      }
    }
    // 卖出单：把多选的买进单建立为联动单（N:M）。单个选择仍同时保留 parent_id 成本分摊。
    if (entrySide.value === 'sell' && saleId && selectedPurchases.value.length) {
      try {
        await api.linkSale({
          sale_id: saleId,
          purchase_ids: selectedPurchases.value,
          title: form.product.trim() || '联动单'
        })
      } catch { incomplete.push('联单关联') }
    }
    try { localStorage.removeItem(DRAFT_KEY) } catch (e) {}
    justSaved.value = true
    const info = { merged: false, product: form.product, id: saleId, editing: isEdit.value, autoGroup: !!autoGroupId, incomplete }
    setTimeout(() => emit('saved', info), 420)
  } catch (e) {
    // 已完结联单的锁定优先于并发冲突：直接把服务端那句中文提示展示出来
    if (e.status === 409 && e.data?.code === 'settled') {
      errMsg.value = e.message || '已完结联单内的记录不能修改'
    } else if (e.status === 409) {
      // 别人先一步保存了同一条：载入服务端最新内容，并把乐观锁基线推到最新版本，
      // 避免用户在自己看不到的旧数据上继续覆盖。
      const cur = e.data?.current
      if (cur) {
        expectedUpdatedAt.value = cur.updated_at || ''
        Object.assign(form, {
          product: cur.product,
          buy_price: cur.buy_price, buy_shipping: cur.buy_shipping, buy_ship_tag: cur.buy_ship_tag,
          buy_order_no: cur.buy_order_no, buy_time: cur.buy_time, buy_counterparty: cur.buy_counterparty,
          sell_price: cur.sell_price, sell_fee: cur.sell_fee, sell_wuyou_fee: cur.sell_wuyou_fee || 0,
          sell_net: cur.sell_net, sell_shipping: cur.sell_shipping, sell_ship_tag: cur.sell_ship_tag,
          sell_order_no: cur.sell_order_no, sell_time: cur.sell_time, sell_counterparty: cur.sell_counterparty,
          note: cur.note, source: cur.source || '', owner: cur.owner || '', quantity: cur.quantity || 1
        })
      }
      errMsg.value = '这条记录已被其他成员修改，已载入最新内容，请核对后重新保存'
    } else {
      errMsg.value = e.message || '保存失败'
    }
  } finally {
    if (!justSaved.value) saving.value = false
  }
}

// ---------- 移动端抽屉下拉关闭 ----------
// 与遮罩点击同等语义：从手柄区下拉超过阈值即关闭；
// 内容滚动未到顶时不接管手势，避免与列表滚动冲突。
const panelEl = ref(null)
const bodyEl = ref(null)
let dragY = null
let dragTop = 0
function onHeadDown(e) {
  if (window.innerWidth > 600) return
  if (!bodyEl.value || bodyEl.value.scrollTop > 0) return
  dragY = e.touches[0].clientY
  dragTop = 0
}
function onHeadMove(e) {
  if (dragY == null) return
  const d = e.touches[0].clientY - dragY
  if (d <= 0) return
  e.preventDefault() // 只拦截下拉，阻止背景滚动
  dragTop = Math.min(160, d * 0.55)
  if (panelEl.value) panelEl.value.style.transform = `translateY(${dragTop}px)`
}
function onHeadEnd() {
  if (dragY == null) return
  // 与确认框 / 回收站统一：跟手位移 > 64px（约手指下拉 116px）即关闭
  const close = dragTop > 64
  dragY = null
  if (!panelEl.value) return
  if (close) {
    panelEl.value.style.transition = ''
    // 清掉跟手位移，否则内联 transform 会压过 Vue Transition 的 leave-to，退场卡在半路
    panelEl.value.style.transform = ''
    emit('close')
    return
  }
  panelEl.value.style.transition = 'transform var(--dur-2) var(--ease)'
  panelEl.value.style.transform = ''
  setTimeout(() => {
    if (panelEl.value) panelEl.value.style.transition = ''
  }, 260)
}
</script>

<template>
  <div ref="maskEl" class="modal-mask" tabindex="-1" @pointerdown="mask.onPointerDown" @click="mask.onClick" @keydown="onModalKey">
    <div ref="panelEl" class="modal modal-wide" :class="{ 'is-readonly': readonly, 'shake': shaking, 'has-err': !!errMsg }">
      <div class="modal-head" @touchstart.passive="onHeadDown" @touchmove="onHeadMove" @touchend="onHeadEnd">
        <h3>
          {{ readonly ? '查看商品记录' : (isEdit ? '编辑商品记录' : '上传截图 · 录入商品') }}
          <span class="modal-shop-tag">
            <i :style="{ background: shopColor }" aria-hidden="true"></i>{{ shopName }}
          </span>
        </h3>
        <button class="x" @click="emit('close')">×</button>
      </div>

      <div ref="bodyEl" class="modal-body">
        <!-- 上传识别区：仅新增模式 -->
        <template v-if="!isEdit">
          <div class="engine-bar">
            <span class="badge" :class="engine === 'local' ? 'badge-ok' : 'badge-buy'">
              <span class="dot"></span>当前引擎：{{ engineName }}
            </span>
            <span class="muted" style="font-size:12px">
              {{ engine === 'local' ? '离线运行，无需 API Key，图片不上传' : '调用你配置的视觉大模型，识别更准' }}
            </span>
            <span class="link" style="margin-left:auto;font-size:12px" @click="$emit('open-settings')">去设置切换</span>
          </div>

          <!-- 识别诊断条：构建版本 + 各关键字段是否真的填进表单，便于核对加载的是不是最新版 -->
          <div class="diag-bar">
            <span class="diag-build">构建 {{ buildVersion }}</span>
            <template v-if="recDiag">
              <span class="diag-fields">
                <template v-for="f in diagFields" :key="f.key">
                  {{ f.label }}
                  <b :class="['diag-check', recDiag[f.key] ? 'ok' : 'miss']">
                    <svg class="ck-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"><path :d="recDiag[f.key] ? CK : XK" /></svg>
                  </b>
                </template>
              </span>
              <span v-if="recDiag.rawLen" class="diag-raw">原文 {{ recDiag.rawLen }} 字</span>
              <span v-if="recDiag.sliced" class="diag-salvage">已分段重识别</span>
              <span v-if="recDiag.salvaged" class="diag-salvage">兜底补 {{ recDiag.salvaged }} 项</span>
            </template>
            <span v-else class="diag-raw">上传截图后显示识别诊断</span>
          </div>
          <div v-if="recDiag && (!recDiag.order || !recDiag.time || !recDiag.cp)" class="tip diag-warn">
            部分字段未取到，已自动展开下方「OCR 原文」供核对；请手动补填缺失项后保存。
          </div>
          <div v-if="recWarn" class="tip diag-warn amount-warn">{{ recWarn }}</div>

          <div v-if="!file" class="drop" @click="$refs.fileInput.click()" @dragover.prevent @drop.prevent="onDrop">
            <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 15l5-5 4 4 3-3 6 6"/><circle cx="8.5" cy="9" r="1"/></svg></div>
            <div class="drop-title">点击选择 或 拖拽闲鱼订单截图到这里<HelpTip text="买进单 / 卖出单都可以，识别后按商品名自动合并成一条记录。&#10;截图会随保存一并存入这条记录的附件，之后可在明细里查看。" /></div>
            <input ref="fileInput" type="file" accept="image/*" hidden @change="onFile" />
          </div>

          <div v-else class="preview-row">
            <img :src="previewUrl" class="preview-img" alt="待识别的闲鱼订单截图预览"
                 width="108" height="108" decoding="async" />
            <div class="preview-side">
              <div class="recognize-state">
                <span v-if="recState === 'recognizing'" class="muted"><span class="spin"></span> {{ recMsg }}</span>
                <span v-else-if="recState === 'done'" class="pos"><svg class="ck-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"><path :d="CK" /></svg> {{ recMsg }}</span>
                <span v-else-if="recState === 'error'" class="neg">! {{ recMsg }}</span>
              </div>
              <div v-if="recState === 'done'" class="ocr-feedback">
                <span class="muted">识别结果准吗？</span>
                <button class="btn btn-sm btn-ghost" @click="sendFeedback(true)">准确</button>
                <button class="btn btn-sm btn-ghost" @click="sendFeedback(false)">需修正</button>
                <span v-if="feedbackMsg" class="muted">{{ feedbackMsg }}</span>
              </div>
              <div v-if="recState === 'recognizing' && recProgress > 0" class="ocr-progress">
                <div class="ocr-progress-bar" :style="{ width: recProgress + '%' }"></div>
              </div>
              <div class="preview-actions">
                <div class="preview-actions-group">
                  <button class="btn btn-sm" @click="doRecognize()" :disabled="recState === 'recognizing'"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg>重新识别</button>
                  <button class="btn btn-sm" @click="switchEngineAndRetry" :disabled="recState === 'recognizing'"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.9 4.6L18.5 9.5l-4.6 1.9L12 16l-1.9-4.6L5.5 9.5l4.6-1.9L12 3z"/><path d="M18.5 16l.6 1.6 1.6.6-1.6.6-.6 1.6-.6-1.6-1.6-.6 1.6-.6.6-1.6z"/></svg>改用{{ engine === 'local' ? '视觉大模型' : '本地 OCR' }}识别</button>
                </div>
                <div class="preview-actions-group">
                  <button class="btn btn-sm" @click="$refs.fileInput2.click()"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 15l4.5-4.5 3 3 3.5-3.5 4 4"/><circle cx="8.5" cy="9" r="1"/></svg>换一张</button>
                  <button class="btn btn-sm btn-ghost btn-danger btn-remove" @click="clearFile"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/><path d="M10 11v6M14 11v6"/></svg>移除</button>
                </div>
                <input ref="fileInput2" type="file" accept="image/*" hidden @change="onFile" />
              </div>
              <div v-if="recState === 'error'" class="tip err">
                识别未成功。可点「改用{{ engine === 'local' ? '视觉大模型' : '本地 OCR' }}识别」重试，或直接在下方手动填写全部字段后保存。
              </div>
              <div v-else-if="mergedHint" class="tip">{{ mergedHint }}</div>
              <div v-if="ocrRaw" class="raw-toggle">
                <span class="link" @click="showRaw = !showRaw">{{ showRaw ? '收起' : '查看' }} OCR 原文（核对用）</span>
                <pre v-if="showRaw" class="raw-text">{{ ocrRaw }}</pre>
              </div>
            </div>
          </div>

          <!-- 本次录入侧 -->
          <div class="entry-side">
            <span class="es-label">本次录入到</span>
            <div class="seg seg-sm">
              <button :class="{ 'on-buy': entrySide === 'buy' }" @click="switchEntrySide('buy')">买进侧</button>
              <button :class="{ 'on-sell': entrySide === 'sell' }" @click="switchEntrySide('sell')">卖出侧</button>
              <button :class="{ 'on-both': entrySide === '' }" @click="switchEntrySide('')">两侧一起填</button>
            </div>
            <HelpTip text="卖出单会按商品名自动关联到对应的买进单（可下方选择「不关联」）；买进单则新建一条。" />
          </div>
          <label class="merge-switch" v-if="!isEdit">
            <input type="checkbox" v-model="allowMerge" aria-label="自动关联同名买进单" />
            <span>自动关联同名买进单</span>
            <HelpTip text="关闭则本单作为独立卖出记录，不关联任何买进单。" />
          </label>

          <!-- N:M 联动单：卖出时可搜索 / 相似推荐 / 多选多个买进单 -->
          <div v-if="entrySide === 'sell'" class="linkbox">
            <div class="linkbox-head">
              <div>
                <b>联动买进单</b>
                <HelpTip text="可多选，一个卖单可对应多个买单。" />
              </div>
              <span class="badge badge-ok" v-if="selectedPurchases.length"><span class="dot"></span>已选 {{ selectedPurchases.length }} 个</span>
            </div>
            <div class="muted" style="font-size:11.5px;margin:-4px 0 8px">已完结联单里的买单不可选，如需补充请先在「联动单」页重新开启该联单</div>
            <!-- 拉取失败必须说出来：空列表看起来就是「没有可选的买单」，
                 用户会转而去改数据，而真实原因只是接口没回来 -->
            <div v-if="linkboxError" class="tip err linkbox-err" role="alert">
              <span>{{ linkboxError }}，暂时无法判断哪些买单已被完结联单锁定</span>
              <button class="btn btn-sm" @click="retryLinkbox">重试</button>
            </div>
            <div class="linkbox-tools">
              <input class="input" v-model="purchaseQuery" placeholder="搜索买进单：商品 / 订单号 / 卖家"
                     aria-label="搜索可关联的买进单" />
              <button class="btn btn-sm" @click="autoSelectSimilar()">匹配相似</button>
              <button class="btn btn-sm" @click="purchasePickerOpen = !purchasePickerOpen">
                {{ purchasePickerOpen ? '收起列表' : '选择买单' }}
              </button>
            </div>
            <div v-if="purchasePickerOpen" class="linkbox-list">
              <label v-for="p in purchaseCandidates" :key="p.id" class="linkbox-item" :class="{ on: selectedPurchases.includes(p.id) }">
                <input type="checkbox" :checked="selectedPurchases.includes(p.id)" @change="togglePurchase(p.id)"
                       :aria-label="`关联买进单 ${p.product || '未命名商品'}`" />
                <div class="linkbox-main">
                  <div class="linkbox-title">{{ p.product || '未命名商品' }}</div>
                  <div class="muted" style="font-size:11px">{{ p.buy_order_no || '—' }} · {{ p.buy_time || '—' }}</div>
                </div>
                <span v-if="selectedPurchases.includes(p.id)" class="linkbox-check" aria-hidden="true"><svg class="ck-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"><path :d="CK" /></svg></span>
                <b class="num">{{ fmtMoneyPlain(p.buy_price) }}</b>
              </label>
              <div v-if="!purchaseCandidates.length && !purchaseLoading && !linkboxError" class="muted" style="padding:12px;font-size:12px">没有匹配的买进单</div>
              <div v-else-if="purchaseLoading" class="muted" style="padding:12px;font-size:12px">正在加载买进单…</div>
            </div>
            <div v-if="selectedPurchases.length && !purchasePickerOpen" class="linkbox-chips">
              <span v-for="id in selectedPurchases" :key="id" class="linkbox-chip" @click="togglePurchase(id)">
                {{ (purchasePool.find(p => p.id === id) || {}).product || id }} ×
              </span>
            </div>
          </div>

          <!-- 1:N 关联状态 / 相似候选选择 -->
          <div v-if="entrySide === 'sell' && !isEdit" class="match-box">
            <div v-if="matchedParent" class="match-linked">
              <span class="badge badge-ok"><span class="dot"></span>已关联买进单：「{{ matchedParent.product }}」</span>
              <span class="muted" style="font-size:12px">卖家 {{ matchedParent.buy_counterparty || '—' }} · 买进价 {{ fmtMoneyPlain(matchedParent.buy_price) }} · 已挂 {{ matchedParent.child_count || 0 }} 笔</span>
              <button class="link link-mini" @click="clearMatch(); mergedHint=''">取消关联</button>
            </div>
            <div v-if="showMatchPicker" class="match-picker">
              <div class="mp-title">找到 {{ matchCandidates.length }} 个相似买进单，选一个关联：</div>
              <div class="mp-list">
                <button v-for="c in matchCandidates" :key="c.id" class="mp-item" @click="chooseCandidate(c.id)">
                  <span class="mp-name">{{ c.product }}</span>
                  <span class="mp-meta">卖家 {{ c.buy_counterparty || '—' }} · 买进价 {{ fmtMoneyPlain(c.buy_price) }} · {{ c.buy_time ? c.buy_time.slice(0, 10) : '—' }} · 已挂 {{ c.child_count || 0 }} 笔</span>
                </button>
              </div>
              <button class="btn btn-sm btn-ghost" @click="skipMatch">不关联，独立新建</button>
            </div>
          </div>
        </template>

        <!-- 商品名 -->
        <div class="field" style="margin-top:16px">
          <label>商品名称 <span class="req">*</span><HelpTip text="同名是自动配对的依据：买进侧与卖出侧商品名一致时，会合并成一条记录并算利润" /></label>
          <input class="input" v-model="form.product" placeholder="如：网心云OEC-Turbo盒子" aria-label="商品名称"
                 :class="{ 'is-err': errors.product }" @input="clearErr('product')" />
          <span v-if="errors.product" class="field-err">{{ errors.product }}</span>
          <div v-if="priceHint && !readonly" class="price-hint">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>
            <span>
              这件卖过 <b>{{ priceHint.count }}</b> 单 · 上次
              <b class="num">{{ fmtMoney(priceHint.last) }}</b>
              <template v-if="priceHint.count > 1"> · 均价 <b class="num">{{ fmtMoney(priceHint.avg) }}</b></template>
              <template v-if="priceHint.last_cost > 0"> · 上次成本 <b class="num">{{ fmtMoney(priceHint.last_cost) }}</b></template>
            </span>
            <span class="price-hint-acts">
              <button type="button" class="link" @click="form.sell_price = priceHint.last">用上次价</button>
              <button v-if="priceHint.last_cost > 0" type="button" class="link" @click="form.unit_cost = priceHint.last_cost">用上次成本</button>
            </span>
          </div>
        </div>

        <!-- 货物来源：买卖两侧都可能有（卖单可能是代卖/回收来的货），放在顶层 -->
        <div class="field" style="margin-top:12px">
          <label>货物来源<HelpTip text="这批货是怎么来的：自采=自己采购，回收=从回收商收，寄卖=代卖别人的货（无进货成本，卖出后钱归寄卖人），代采=垫钱帮别人买（成本归委托人），闲鱼=从闲鱼买入。寄卖 / 代采都不计入自己的经营口径，只在「寄卖」页单独记账。闲鱼同步进来的单据自动记为「闲鱼」，卖单也能单独标。" /></label>
          <select class="select" v-model="form.source" aria-label="货物来源">
            <option value="">未标注</option>
            <option value="自采">自采</option>
            <option value="回收">回收</option>
            <option value="寄卖">寄卖</option>
            <option value="代采">代采</option>
            <option value="闲鱼">闲鱼</option>
          </select>
        </div>

        <!-- 归属：寄卖 / 代采必填对方是谁；其它来源可选填。输入单字自动匹配，点击输入框列出全部 -->
        <div class="field" style="margin-top:12px">
          <label>归属{{ form.source === '寄卖' || form.source === '代采' ? '（必填）' : '' }}<HelpTip text="这单货是谁的。寄卖填寄卖人、代采填委托人（如「张三」），之后能在「寄卖」页单独查他寄卖了多少钱 / 应还你多少钱、多少单、卖了什么；其它单据也能选填。输入关键字自动匹配，点输入框看全部已有归属。" /></label>
          <div class="owner-pick">
            <input class="input" v-model="form.owner" placeholder="输入或选择归属人，如 张三" aria-label="归属人"
                   autocomplete="off" @focus="ownerOpen = true" @input="ownerOpen = true" @blur="ownerBlur"
                   @keydown.enter.prevent="form.owner.trim() && pickOwner(form.owner)" />
            <div v-if="ownerAll.length" class="owner-quick">
              <button v-for="n in ownerAll.slice(0, 8)" :key="'q'+n" type="button" class="owner-quick-chip"
                      :class="{ on: form.owner === n }" @click="form.owner = n">{{ n }}</button>
            </div>
            <div v-if="ownerOpen" class="owner-list">
              <button v-for="n in ownerCandidates" :key="n" type="button" class="owner-item" @pointerdown.prevent="pickOwner(n)">{{ n }}</button>
              <button v-if="form.owner.trim() && !ownerCandidates.includes(form.owner.trim())"
                      type="button" class="owner-item owner-new" @pointerdown.prevent="pickOwner(form.owner.trim())">
                新增「{{ form.owner.trim() }}」<span>Enter</span>
              </button>
            </div>
          </div>
        </div>

        <!-- 买进信息：新增时选卖出侧则隐藏；编辑时按记录类型显示（仅买单 / 独立单显示，卖单隐藏） -->
        <div class="side-box buy" v-show="isEdit ? editShowBuy : entrySide !== 'sell'">
          <div class="side-title"><span class="side-dot buy"></span>买进信息 <span class="side-sub">我花了多少钱</span>
            <button class="side-toggle" :class="{ open: !collapsed.buy }" :aria-expanded="!collapsed.buy" @click="collapsed.buy = !collapsed.buy">
              {{ collapsed.buy ? '展开' : '收起' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
            </button>
          </div>
          <div class="side-content" :class="{ collapsed: collapsed.buy }"><div class="side-content-inner">
          <div class="form-grid tight">
            <div class="field">
              <label>买进价</label>
              <input class="input num" type="number" step="0.01" v-model.number="form.buy_price" placeholder="0.00"
                     aria-label="买进价" :class="{ 'is-err': errors.buy_price }" @input="clearErr('buy_price')" />
              <span v-if="errors.buy_price" class="field-err">{{ errors.buy_price }}</span>
            </div>
            <div class="field">
              <label>件数<HelpTip text="一单多件：同一单里买了多个同款时，填实际数量" /></label>
              <input class="input num" type="number" step="1" min="1" v-model.number="form.quantity" placeholder="1"
                     aria-label="件数" :class="{ 'is-err': errors.quantity }" @input="clearErr('quantity')" />
              <span v-if="errors.quantity" class="field-err">{{ errors.quantity }}</span>
            </div>
            <div class="field">
              <label>买进运费</label>
              <input class="input num" type="number" step="0.01" v-model.number="form.buy_shipping" placeholder="0.00" aria-label="买进运费"
                     :disabled="form.buy_ship_tag === '自提'" :class="{ 'is-err': errors.buy_shipping }" @input="clearErr('buy_shipping')" />
              <span v-if="form.buy_ship_tag === '自提'" class="field-hint muted">自提：买运费固定 0</span>
              <span v-if="errors.buy_shipping" class="field-err">{{ errors.buy_shipping }}</span>
            </div>
            <div class="field full">
              <label>运费标签</label>
              <div class="tag-row">
                <select class="select" :value="customBuy ? '__new__' : form.buy_ship_tag" aria-label="买进运费标签"
                        @change="onTagSelect('buy', $event)">
                  <option value="">无标签</option>
                  <option v-for="t in tags" :key="t.id" :value="t.name">{{ t.name }}</option>
                  <option value="__new__">+ 新增标签…</option>
                </select>
                <template v-if="customBuy">
                  <input class="input" v-model="newTag" placeholder="输入新标签名" aria-label="新增买进运费标签"
                         @keyup.enter="saveNewTag" />
                  <button class="btn btn-sm" @click="saveNewTag">添加</button>
                </template>
              </div>
            </div>
            <div class="field">
              <label>买进订单号</label>
              <input class="input mono" v-model="form.buy_order_no" placeholder="纯数字，可检索" aria-label="买进订单号" />
            </div>
            <div class="field">
              <label>卖家昵称</label>
              <input class="input" v-model="form.buy_counterparty" placeholder="对方昵称，可检索" aria-label="卖家昵称" />
            </div>
            <div class="field full">
              <label>买进时间</label>
              <input class="input" type="datetime-local" step="1" v-model="buyTimeLocal" aria-label="买进时间" />
              <span v-if="timeApprox" class="time-approx-tip">仅还原出日期（来自支付宝交易号），时分秒为 00:00:00，可对照截图手动补全</span>
            </div>
          </div>
          </div></div>
        </div>

        <!-- 卖出信息：新增时选买进侧则隐藏；编辑时按记录类型显示（仅卖单 / 独立单显示，买单隐藏） -->
        <div class="side-box sell" v-show="isEdit ? editShowSell : entrySide !== 'buy'">
          <div class="side-title"><span class="side-dot sell"></span>卖出信息 <span class="side-sub">我收了多少钱</span>
            <button class="side-toggle" :class="{ open: !collapsed.sell }" :aria-expanded="!collapsed.sell" @click="collapsed.sell = !collapsed.sell">
              {{ collapsed.sell ? '展开' : '收起' }}<svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
            </button>
          </div>
          <div class="side-content" :class="{ collapsed: collapsed.sell }"><div class="side-content-inner">
          <div class="form-grid tight">
            <div class="field">
              <label>成交价</label>
              <input class="input num" type="number" step="0.01" v-model.number="form.sell_price" placeholder="0.00"
                     aria-label="成交价" :class="{ 'is-err': errors.sell_price }" @input="clearErr('sell_price'); recalcNet()" />
              <span v-if="errors.sell_price" class="field-err">{{ errors.sell_price }}</span>
            </div>
            <div class="field" v-if="!matchedParent">
              <label>单件成本<HelpTip text="这批货的进货成本（含买运费分摊）。关联了买进单时会自动带出，没关联就直接填这里；填了它，明细和统计里的利润才会扣掉成本。已经在联单里、联单又有买进单的卖单，会按成交价占比自动分摊一份成本，不用手填。" /></label>
              <input class="input num" type="number" step="0.01" v-model.number="form.unit_cost" placeholder="0.00"
                     aria-label="单件成本" :disabled="form.cost_free" />
              <label class="cf-check">
                <input type="checkbox" v-model="form.cost_free" aria-label="这单没有货品成本" />
                这单没有货品成本（代卖 / 寄卖 / 赠送）
              </label>
              <span v-if="allocCost > 0 && !Number(form.unit_cost)" class="field-hint muted">
                联单自动分摊 {{ fmtMoneyPlain(allocCost) }} / 件（只影响核算，不写回单据）
              </span>
            </div>
            <div class="field">
              <label>软件服务费<HelpTip text="成本项，按软件服务费 + 基础软件服务费合计，从成交额里扣除" /></label>
              <input class="input num" type="number" step="0.01" v-model.number="form.sell_fee" placeholder="0.00"
                     aria-label="软件服务费" :class="{ 'is-err': errors.sell_fee }" @input="clearErr('sell_fee'); recalcNet()" />
              <span v-if="errors.sell_fee" class="field-err">{{ errors.sell_fee }}</span>
            </div>
            <div class="field">
              <label>无忧卖类目软件服务费<HelpTip text="成本项，从成交额里扣除" /></label>
              <input class="input num" type="number" step="0.01" v-model.number="form.sell_wuyou_fee" placeholder="0.00"
                     aria-label="无忧卖类目软件服务费" :class="{ 'is-err': errors.sell_wuyou_fee }"
                     @input="clearErr('sell_wuyou_fee'); recalcNet()" />
              <span v-if="errors.sell_wuyou_fee" class="field-err">{{ errors.sell_wuyou_fee }}</span>
            </div>
            <div class="field">
              <label>到手价<HelpTip text="自动计算：成交价 − 卖运费 − 软件服务费 − 无忧卖类目费。改了其中任一项会立刻重算；平台有补贴/优惠时可以手动改，保存以这里为准。到手价只用于展示与对账，不参与净利润计算。" /></label>
              <input class="input num" type="number" step="0.01" v-model.number="form.sell_net" placeholder="0.00"
                     aria-label="到手价" :class="{ 'is-err': errors.sell_net }" @input="clearErr('sell_net')" />
              <span v-if="errors.sell_net" class="field-err">{{ errors.sell_net }}</span>
            </div>
            <div class="field">
              <div class="label-row">
                <label>卖出运费<HelpTip text="成本项，从利润里扣除" /></label>
                <a class="link link-mini" @click="autoNet">按 成交价−服务费 算到手</a>
              </div>
              <input class="input num" type="number" step="0.01" v-model.number="form.sell_shipping" placeholder="0.00" aria-label="卖出运费"
                     :disabled="form.sell_ship_tag === '自提'" :class="{ 'is-err': errors.sell_shipping }" @input="clearErr('sell_shipping'); recalcNet()" />
              <span v-if="form.sell_ship_tag === '自提'" class="field-hint muted">自提：卖运费固定 0</span>
              <span v-if="errors.sell_shipping" class="field-err">{{ errors.sell_shipping }}</span>
            </div>
            <div class="field full">
              <label>运费标签</label>
              <div class="tag-row">
                <select class="select" :value="customSell ? '__new__' : form.sell_ship_tag" aria-label="卖出运费标签"
                        @change="onTagSelect('sell', $event)">
                  <option value="">无标签</option>
                  <option v-for="t in tags" :key="t.id" :value="t.name">{{ t.name }}</option>
                  <option value="__new__">+ 新增标签…</option>
                </select>
                <template v-if="customSell">
                  <input class="input" v-model="newTag" placeholder="输入新标签名" aria-label="新增卖出运费标签"
                         @keyup.enter="saveNewTag" />
                  <button class="btn btn-sm" @click="saveNewTag">添加</button>
                </template>
              </div>
            </div>
            <div class="field">
              <label>卖出订单号</label>
              <input class="input mono" v-model="form.sell_order_no" placeholder="纯数字，可检索" aria-label="卖出订单号" />
            </div>
            <div class="field">
              <label>买家昵称</label>
              <input class="input" v-model="form.sell_counterparty" placeholder="对方昵称，可检索" aria-label="买家昵称" />
            </div>
            <div class="field full">
              <label>卖出时间</label>
              <input class="input" type="datetime-local" step="1" v-model="sellTimeLocal" aria-label="卖出时间" />
              <span v-if="timeApprox" class="time-approx-tip">仅还原出日期（来自支付宝交易号），时分秒为 00:00:00，可对照截图手动补全</span>
            </div>
          </div>
          </div></div>
        </div>

        <div class="field" style="margin-top:14px">
          <label>备注</label>
          <input class="input" v-model="form.note" placeholder="地址、支付宝交易号等任意信息，均可被检索" aria-label="备注" />
        </div>

        <div v-if="isEdit" class="field attachment-field">
          <label>订单截图附件</label>
          <div class="attachment-row">
            <img v-if="attachmentName" :src="api.attachmentUrl(props.record.id, { w: 240, file: attachmentName })" class="attachment-thumb"
                 alt="已有订单截图，点击查看大图" width="64" height="64" role="button" tabindex="0" title="点击查看大图"
                 @click="viewerSrc = api.attachmentUrl(props.record.id, { file: attachmentName })"
                 @keyup.enter="viewerSrc = api.attachmentUrl(props.record.id, { file: attachmentName })" />
            <label class="btn btn-sm btn-quiet">
              上传截图
              <input type="file" accept="image/*" hidden @change="onAttachment" />
            </label>
            <span class="muted">{{ attachmentName || '暂无附件' }}</span>
          </div>
        </div>

        <div class="formula">
          <template v-if="externalGoods">
            <span class="f-label">外部结算 · 不计自有成本</span>
            <span class="f-eq">{{ form.source === '代采' || form.source === '代买' ? '代采垫付款在「寄卖」页向归属人收款' : '寄卖款在「寄卖」页按归属人结算' }}</span>
            <span class="f-result">{{ form.source === '代采' || form.source === '代买' ? '去代采收款' : '去寄卖结算' }}</span>
          </template>
          <template v-else-if="hasSell">
            <span class="f-label">本条利润</span>
            <span class="f-eq" v-if="form.parent_id && form.unit_cost">
              成交 {{ fmtMoneyPlain(revenue) }} − 单件成本 {{ fmtMoneyPlain(form.unit_cost) }}×{{ form.quantity || 1 }} − 卖运费 {{ fmtMoneyPlain(form.sell_shipping) }} − 服务费 {{ fmtMoneyPlain(feeAll) }}
            </span>
            <span class="f-eq" v-else>
              成交 {{ fmtMoneyPlain(revenue) }} − 买价 {{ fmtMoneyPlain(form.buy_price) }} − 买运费 {{ fmtMoneyPlain(form.buy_shipping) }} − 卖运费 {{ fmtMoneyPlain(form.sell_shipping) }} − 服务费 {{ fmtMoneyPlain(feeAll) }}
            </span>
            <span class="f-result num" :class="profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(profit) }}</span>
          </template>
          <template v-else>
            <span class="f-label">待售 · 尚未卖出</span>
            <span class="f-eq">买进占用成本（买价 + 买运费）</span>
            <span class="f-result num" style="color:var(--blue)">{{ fmtMoney(holding) }}</span>
          </template>
        </div>

      </div>

      <ImageViewer v-if="viewerSrc" :src="viewerSrc" @close="viewerSrc = ''" />

      <div class="modal-foot record-foot">
        <div v-if="errMsg" class="modal-err" role="alert">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="flex:0 0 auto;margin-top:1px"><circle cx="12" cy="12" r="9"/><line x1="12" y1="8" x2="12" y2="13"/><line x1="12" y1="16.5" x2="12.01" y2="16.5"/></svg>
          <span>{{ errMsg }}</span>
        </div>
        <span class="muted foot-hint">利润 = 成交 − 货品成本（单件成本或联单分摊） − 买运费 − 卖运费 − 服务费</span>
        <template v-if="readonly">
          <button class="btn btn-quiet" @click="emit('close')">关闭</button>
          <button class="btn btn-primary" @click="startEdit">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-right:5px"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>
            编辑
          </button>
        </template>
        <template v-else>
          <button class="btn btn-quiet" @click="emit('close')">取消</button>
          <button class="btn btn-primary" @click="save" :disabled="saving" :class="{ 'is-saved': justSaved }">
            <span v-if="saving && !justSaved" class="spin"></span>{{ justSaved ? '✓ 已保存' : (saving ? '保存中…' : (isEdit ? '保存修改' : '保存记录')) }}
          </button>
        </template>
      </div>
    </div>
  </div>
</template>
