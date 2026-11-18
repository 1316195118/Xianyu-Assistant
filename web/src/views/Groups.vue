<script setup>
import { ref, reactive, computed, onMounted, watch, nextTick } from 'vue'
import { api } from '../api'
import { confirm } from '../confirm'
import { undoAction } from '../feedback'
import { fmtMoney, shortTime, todayStr } from '../utils'
import HelpTip from '../components/HelpTip.vue'

const props = defineProps({
  refreshKey: { type: Number, default: 0 },
  focusGroupId: { type: Number, default: null }
})
const emit = defineEmits(['notify', 'edit', 'view', 'focused'])

const groups = ref([])
const loading = ref(false)
const error = ref('')
const editingId = ref(null)
const showEditor = ref(false)
const saving = ref(false)
const focusedId = ref(null)   // 从明细联动过来时高亮 + 展开全部关联记录
const expandedId = ref(null)
const merging = ref(false)

const form = reactive({ title: '', note: '', item_ids: [] })
const editorEl = ref(null)
const q = ref('')
const side = ref('')
const candidates = ref([])
const candidateLoading = ref(false)
const selectedMap = ref({})
// 已选记录的商品名（用于推荐标题）。搜索切换候选后，旧的选中项会离开 candidates，
// 所以这里单独留一份 id -> {product, kind}
const pickedInfo = ref({})
let searchTimer = null

async function load() {
  loading.value = true; error.value = ''
  try { groups.value = (await api.groups()).items || [] }
  catch (e) { error.value = e.message || '加载失败' }
  finally { loading.value = false }
}
onMounted(load)
watch(() => props.refreshKey, load)

// 合并重复联单：把“共享同一条记录（同一买单）”的多个联单自动合并为一个
async function mergeDuplicates() {
  if (merging.value) return
  const ok = await confirm({
    title: '合并重复联单？',
    message: '将把共享同一买单 / 记录的多个联单合并为一个。',
    detail: ['只删除重复的联单分组，其中的买卖记录会完整保留。'],
    confirmText: '开始合并', danger: true
  })
  if (!ok) return
  merging.value = true
  try {
    const r = await api.autoMergeGroups()
    await load()
    emit('notify', r.merged_groups > 0 ? `已合并 ${r.merged_groups} 个重复联单` : '没有检测到重复联单')
  } catch (e) {
    emit('notify', '合并失败：' + (e.message || '未知错误'), true)
  } finally {
    merging.value = false
  }
}
watch(() => props.focusGroupId, async (id) => {
  if (!id) return
  if (!groups.value.length) await load()
  focusedId.value = id
  expandedId.value = id
  emit('focused')
  // 滚动到该联单卡片
  setTimeout(() => {
    const el = document.querySelector('[data-group-id="' + id + '"]')
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }, 80)
}, { immediate: true })

const sortedGroups = computed(() => {
  const list = [...groups.value]
  // 默认按净利润从高到低，方便看「什么商品好卖、利润高」
  return list.sort((a, b) => (b.stats?.profit || 0) - (a.stats?.profit || 0))
})
const selectedCount = computed(() => form.item_ids.length)

function rememberItem(it) {
  if (!it || !it.id) return
  pickedInfo.value[it.id] = { product: String(it.product || '').trim(), kind: it.kind }
}

// 选完商品自动给一个标题建议，但**不自动填**——由用户决定要不要采用
const suggestedTitle = computed(() => {
  const ids = form.item_ids
  if (!ids.length) return ''
  const names = ids.map(id => (pickedInfo.value[id] || {}).product || '').filter(Boolean)
  if (!names.length) return ''
  const uniq = [...new Set(names)]
  if (uniq.length === 1) return names.length > 1 ? `${uniq[0]} × ${names.length}` : uniq[0]
  const base = uniq[0].length > 16 ? uniq[0].slice(0, 16) + '…' : uniq[0]
  return `${base} 等 ${ids.length} 件`
})
const roleOf = (it) => it.kind === 'purchase' ? '买单' : '卖单'
const roleClass = (it) => it.kind === 'purchase' ? 'badge-buy' : 'badge-sell'

// 联单卡片里的展示行：真实明细 + 本联单自己的外部成交。
// 外面包一层统一结构，模板只认字段，不用在两处判断数据来自哪张表。
function groupRows(g) {
  const rows = (g.items || []).map(it => ({
    key: 'i' + it.id,
    product: it.product,
    kindLabel: roleOf(it),
    kindClass: roleClass(it),
    amount: it.has_sell ? (it.sell_price || it.sell_net) : (it.buy_price || it.unit_cost),
    muted: !it.has_sell && !(it.buy_price || it.unit_cost),
    external: false,
    item: it,                  // 点击这一行直接打开这条记录的详情
    source: it.source || ''
  }))
  for (const e of [...(g.externals || []), ...(g.externals_buy || [])]) {
    rows.push({
      key: 'e' + e.id,
      product: e.product,
      kindLabel: e.kind === 'buy' ? '买单' : '卖单',
      kindClass: 'badge-ext',
      amount: e.amount,
      muted: false,
      external: true,
      item: null,
      source: e.source || ''
    })
  }
  return rows
}

// 联单的货物来源：取联单内「买进侧」的来源。全是同一个就显示它，
// 混着不同来源时显示「多来源」，一个都没标则什么都不显示。
function groupSource(g) {
  const set = new Set()
  for (const it of (g.items || [])) {
    if (it.has_buy && it.source) set.add(it.source)
  }
  for (const e of (g.externals_buy || [])) {
    if (e.source) set.add(e.source)
  }
  if (set.size === 0) return ''
  if (set.size === 1) return [...set][0]
  return '多来源'
}
// 对方昵称：买单看卖家、卖单看买家（与明细列表保持一致）
const counterpartyOf = (it) => String((it.has_sell ? it.sell_counterparty : it.buy_counterparty) || '').trim()

function openCreate() {
  editingId.value = null
  form.title = ''; form.note = ''; form.item_ids = []
  q.value = ''; side.value = ''; selectedMap.value = {}; pickedInfo.value = {}
  externals.value = []
  externalsBuy.value = []
  extDirty.value = { sell: false, buy: false }
  showEditor.value = true
  loadCandidates()
  scrollToEditor()
}
// 编辑器是页面顶部的一块卡片：打开后必须自己滚过去，
// 否则点了「编辑」看着像什么都没发生（联单列表在下面，视口停在原处）。
async function scrollToEditor() {
  await nextTick()
  const el = editorEl.value
  if (!el) return
  // 顶栏是 sticky 的，scrollIntoView 会把标题顶到栏下面，这里留出 84px
  const y = el.getBoundingClientRect().top + window.scrollY - 84
  window.scrollTo({ top: Math.max(y, 0), behavior: 'smooth' })
}
async function openEdit(g) {
  if (blockIfSettled(g, '修改')) return
  editingId.value = g.id
  form.title = g.title; form.note = g.note || ''
  // 外部成交单独一块维护：它们只活在这个联单里，不占候选、不进明细
  applyExternals(g.externals)
  extDirty.value = { sell: false, buy: false }
  form.item_ids = [...(g.item_ids || [])]
  selectedMap.value = {}
  pickedInfo.value = {}
  form.item_ids.forEach(id => { selectedMap.value[id] = true })
  for (const it of (g.items || [])) rememberItem(it)
  q.value = ''; side.value = ''
  showEditor.value = true
  scrollToEditor()
  await loadCandidates()
  // 已选记录保证出现在候选列表里（即使不在最近 60 条）
  for (const it of (g.items || [])) {
    if (!candidates.value.some(c => c.item.id === it.id)) {
      candidates.value.unshift({ item: it, score: 100, reason: '已选' })
    }
  }
}
async function closeEditor() {
  if (extDirty.value.sell || extDirty.value.buy) {
    const ok = await confirm({
      title: '外部成交还没保存',
      message: '现在收起会丢掉刚填的外部买入 / 卖出。',
      detail: ['每块右上角都有独立的「保存」按钮，点一下就会写进这张联单。'],
      confirmText: '丢弃并收起', danger: true
    })
    if (!ok) return
  }
  showEditor.value = false
}

// ---------- 外部成交（线下 / 其他平台，没有闲鱼订单号） ----------
// 只存在这个联单里：不写明细、不进全局统计，只在本联单的销售额 / 货品成本里生效。
// 买入和卖出对称，两边都单独记一笔，并在卡片上用「外部」标签区分。
const externals = ref([])     // 外部卖出
const externalsBuy = ref([])  // 外部买入

const extSaving = ref('')
const extDirty = ref({ sell: false, buy: false })

const extList = (key) => (key === 'buy' ? externalsBuy.value : externals.value)
const mapExternal = (e) => ({
  id: e.id,
  product: e.product || '',
  amount: e.amount || 0,
  quantity: e.quantity || 1,
  counterparty: e.counterparty || '',
  time: String(e.time || '').slice(0, 10),
  note: e.note || ''
})
function applyExternals(list) {
  externals.value = (list || []).filter(e => e.kind !== 'buy').map(mapExternal)
  externalsBuy.value = (list || []).filter(e => e.kind === 'buy').map(mapExternal)
}
function markExtDirty(key) { extDirty.value[key === 'buy' ? 'buy' : 'sell'] = true }
function addExternal(key) {
  extList(key).push({
    id: 0, product: '', amount: null, quantity: 1,
    counterparty: '', time: todayStr(), note: '', source: ''
  })
  markExtDirty(key)
}
function removeExternal(key, i) { extList(key).splice(i, 1); markExtDirty(key) }

// 每块自己的「保存」：只替换这一块，不碰标题、不碰成员、也不碰另一块。
// 联单还没创建时，先按当前标题 + 已选记录建一张，再把两块一起存进去。
async function saveExtBlock(key) {
  if (extSaving.value) return
  const kind = key === 'buy' ? 'buy' : 'sell'
  const e = externalError()
  if (e) { emit('notify', e, true); return }
  if (selectedCount.value === 0 && !normExternals(externals.value).length && !normExternals(externalsBuy.value).length) {
    emit('notify', '先勾选至少一条记录，或填一笔外部成交', true); return
  }
  if (!form.title.trim()) {
    emit('notify', '先填写联单标题，才能单独保存外部成交', true); return
  }
  extSaving.value = key
  try {
    if (!editingId.value) {
      const g = await api.createGroup({
        title: form.title.trim(), note: form.note.trim(), item_ids: form.item_ids,
        externals: normExternals(externals.value), externals_buy: normExternals(externalsBuy.value)
      })
      editingId.value = g.id
      applyExternals(g.externals)
      extDirty.value = { sell: false, buy: false }
      emit('notify', '联单已创建，外部成交已保存')
      load()
      return
    }
    const g = await api.saveGroupExternals(editingId.value, kind, normExternals(extList(key)))
    applyExternals(g.externals)
    extDirty.value[kind === 'buy' ? 'buy' : 'sell'] = false
    emit('notify', kind === 'buy' ? '外部买入已保存' : '外部卖出已保存')
    load()
  } catch (err) {
    emit('notify', err.message || '保存失败', true)
  } finally {
    extSaving.value = ''
  }
}
// 合计只做展示：金额本身就是这一单的总价，数量不参与乘算
const extTotal = (list) => Math.round(list.reduce((s, e) => s + (Number(e.amount) || 0), 0) * 100) / 100

const extBlocks = computed(() => [
  {
    key: 'sell',
    title: '外部卖出',
    hint: '不在闲鱼成交的单；只进这个联单的销售额，不写进明细',
    amountLabel: '卖出金额', partyLabel: '买家 / 渠道', dateLabel: '成交日期',
    empty: '整批货线下卖掉了、闲鱼上没有记录？点「添加一笔」补进来。',
    tip: '金额按整单总价填，数量只作记录',
    items: externals.value,
    total: extTotal(externals.value)
  },
  {
    key: 'buy',
    title: '外部买入',
    hint: '没走闲鱼的进货；只进这个联单的货品成本，不写进明细',
    amountLabel: '买进金额', partyLabel: '卖家 / 渠道', dateLabel: '买进日期',
    empty: '线下收的货、没有闲鱼订单？点「添加一笔」把它记成成本。',
    tip: '金额按整单总价填，数量只作记录',
    items: externalsBuy.value,
    total: extTotal(externalsBuy.value)
  }
])

function normExternals(list) {
  return list
    .filter(e => String(e.product || '').trim() || Number(e.amount) > 0)
    .map(e => ({
      id: e.id || 0,
      product: String(e.product || '').trim(),
      amount: Math.round((Number(e.amount) || 0) * 100) / 100,
      quantity: Math.max(1, parseInt(e.quantity) || 1),
      counterparty: String(e.counterparty || '').trim(),
      time: String(e.time || '').slice(0, 10),
      note: String(e.note || '').trim(),
      source: String(e.source || '')
    }))
}
function externalError() {
  for (const b of extBlocks.value) {
    for (const e of b.items) {
      const hasSomething = String(e.product || '').trim() || Number(e.amount) > 0
      if (!hasSomething) continue
      if (!String(e.product || '').trim()) return b.title + '要填商品 / 说明'
      if (!(Number(e.amount) > 0)) return `${b.title}「${e.product}」的金额要大于 0`
    }
  }
  return ''
}

async function loadCandidates() {
  candidateLoading.value = true
  try {
    const p = { side: side.value }
    if (q.value.trim()) p.q = q.value.trim()
    else if (form.title.trim()) p.similar = form.title.trim()
    candidates.value = (await api.groupCandidates(p)).items || []
  } catch (e) { candidates.value = [] }
  finally { candidateLoading.value = false }
}
function onSearchInput() { clearTimeout(searchTimer); searchTimer = setTimeout(loadCandidates, 300) }
function onTitleInput() { if (!q.value.trim()) { clearTimeout(searchTimer); searchTimer = setTimeout(loadCandidates, 400) } }
function toggleSelect(id) {
  const i = form.item_ids.indexOf(id)
  if (i >= 0) { form.item_ids.splice(i, 1); selectedMap.value[id] = false }
  else {
    form.item_ids.push(id); selectedMap.value[id] = true
    const c = candidates.value.find(x => x.item.id === id)
    if (c) rememberItem(c.item)
  }
}
function autoMatch() {
  // 一键把同名 / 高相似候选加入（相似度 ≥ 80）
  let n = 0
  for (const c of candidates.value) {
    if (c.score >= 80 && !selectedMap.value[c.item.id]) {
      toggleSelect(c.item.id); n++
    }
  }
  emit('notify', n ? `已自动匹配 ${n} 条同名/高相似记录` : '没有找到高相似记录，请手动搜索选择')
}
async function save() {
  if (!form.title.trim()) { emit('notify', '请填写联单标题', true); return }
  const extErr = externalError()
  if (extErr) { emit('notify', extErr, true); return }
  const exts = normExternals(externals.value)
  const extBuys = normExternals(externalsBuy.value)
  // 只有外部成交也能成单：整批货线下买进、线下卖掉、闲鱼一条记录都没有的情况是存在的
  if (!selectedCount.value && !exts.length && !extBuys.length) {
    emit('notify', '请至少勾选一条买单或卖单，或补一笔外部成交', true); return
  }
  saving.value = true
  try {
    const payload = {
      title: form.title.trim(), note: form.note.trim(),
      item_ids: form.item_ids, externals: exts, externals_buy: extBuys
    }
    if (editingId.value) await api.updateGroup(editingId.value, payload)
    else await api.createGroup(payload)
    emit('notify', editingId.value ? '联单已更新' : '联单已创建')
    closeEditor(); load()
  } catch (e) { emit('notify', e.message || '保存失败', true) }
  finally { saving.value = false }
}
async function del(g) {
  if (blockIfSettled(g, '删除')) return
  const ok = await confirm({
    title: '删除联单？',
    message: `「${g.title}」将被移除。`,
    detail: '只删除联单分组，不会删除其中的买卖记录。',
    confirmText: '删除联单', danger: true
  })
  if (!ok) return
  try {
    const res = await api.removeGroup(g.id)
    emit('notify', '联单已删除', false, undoAction(res.undo_token, {
      after: load,
      notify: (t) => emit('notify', t),
      doneText: '已撤销删除'
    }))
    load()
  } catch (e) { emit('notify', '删除失败：' + e.message, true) }
}
const isSettled = (g) => !!g.settled_at
// 已完结联单是已对过账的快照：只能先「重新开启」，再改标题或增减成员
function blockIfSettled(g, action = '修改') {
  if (!isSettled(g)) return false
  emit('notify', `「${g.title || '未命名联单'}」已完结，${action}前请先点「重新开启」`, true)
  return true
}
// 印章上的完结日期：2026-11-14 12:00:00 -> 2026.11.14
const stampDate = (s) => {
  const d = String(s || '').replace('T', ' ').slice(0, 10).replace(/-/g, '.')
  return d || '已结清'
}
async function toggleSettle(g) {
  try {
    const r = await api.settleGroup(g.id, !isSettled(g))
    if (r.ok) {
      emit('notify', r.settled ? '联单已完结，其买单不再计入在库占用资金' : '已重新开启联单')
      load()
    }
  } catch (e) { emit('notify', '操作失败：' + e.message, true) }
}
</script>

<template>
  <div class="page">
    <div v-if="error" class="alert-err">{{ error }} <button class="btn btn-sm" @click="load">重试</button></div>

    <section class="card groups-hero">
      <div>
        <div class="gh-label">联动单 · 哪个商品好卖、利润高<HelpTip text="把同一批货的多个买单 / 卖单挂到一个联单，自动汇总净利润、服务费与运费" /></div>
      </div>
      <div class="hero-actions">
        <button class="btn btn-primary" @click="openCreate"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>新建联单</button>
        <button class="btn btn-ghost" :disabled="merging" @click="mergeDuplicates">{{ merging ? '合并中…' : '合并重复联单' }}</button>
      </div>
    </section>

    <!-- 编辑器 -->
    <section v-if="showEditor" ref="editorEl" class="card editor-card">
      <div class="panel-head"><h3>{{ editingId ? '编辑联单' : '新建联单' }}</h3><button class="btn btn-sm btn-ghost" @click="closeEditor">收起</button></div>
      <div class="editor-grid">
        <div class="field">
          <label>联单标题 <span class="req">*</span><HelpTip text="例如：MikroTik RB5009 这批货" /></label>
          <input class="input" v-model="form.title" @input="onTitleInput" placeholder="输入标题，自动推荐相似商品" />
          <!-- 选完商品后给一个建议标题，用户自己决定要不要用 -->
          <div v-if="suggestedTitle && suggestedTitle !== form.title.trim()" class="title-suggest">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.9 4.6L18.5 9.5l-4.6 1.9L12 16l-1.9-4.6L5.5 9.5l4.6-1.9L12 3z"/></svg>
            <span class="ts-label">建议标题</span>
            <b class="ts-text">{{ suggestedTitle }}</b>
            <button class="btn btn-sm btn-quiet" @click="form.title = suggestedTitle">采用</button>
          </div>
        </div>
        <div class="field">
          <label>备注</label>
          <input class="input" v-model="form.note" placeholder="选填，记录这批货的说明" />
        </div>
      </div>

      <!-- 外部卖出：线下 / 其他平台 / 熟人买走的货，没有闲鱼订单号，但一样是一笔成交。
           只存在这个联单自己的表里：不进明细、不影响全局统计，联单删掉时一起消失。 -->
      <div v-for="b in extBlocks" :key="b.key" class="ext-box">
        <div class="ext-head">
          <div class="ext-title">
            <b>{{ b.title }}<span v-if="extDirty[b.key]" class="ext-dirty">未保存</span></b>
            <HelpTip :text="b.hint" />
          </div>
          <button v-if="b.items.length" class="btn btn-sm"
                  :class="extDirty[b.key] ? 'btn-primary' : 'btn-ghost'"
                  :disabled="!!extSaving" @click="saveExtBlock(b.key)"
                  :title="editingId ? '只保存这一块，不影响标题和其他内容' : '会先按当前标题创建联单，再保存'">
            {{ extSaving === b.key ? '保存中…' : (extDirty[b.key] ? '保存' : '已保存') }}
          </button>
          <button class="btn btn-sm btn-ghost" @click="addExternal(b.key)">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            添加一笔
          </button>
        </div>
        <div v-if="b.items.length" class="ext-list">
          <!-- 每笔都按「录入商品」那套字段式布局：一行两列、有字段名，填的时候不用猜哪格填什么 -->
          <div v-for="(e, i) in b.items" :key="e.id || (b.key + i)" class="ext-card">
            <div class="ext-card-head">
              <span class="ext-card-idx">第 {{ i + 1 }} 笔</span>
              <button class="icon-btn icon-btn-danger" @click="removeExternal(b.key, i)" :title="'移除这笔' + b.title" :aria-label="'移除这笔' + b.title">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="6" y1="6" x2="18" y2="18"/><line x1="18" y1="6" x2="6" y2="18"/></svg>
              </button>
            </div>
            <div class="form-grid tight">
              <div class="field full">
                <label>商品 / 说明</label>
                <input class="input" v-model="e.product" placeholder="卖了什么 / 买了什么" @input="markExtDirty(b.key)" />
              </div>
              <div class="field">
                <label>{{ b.amountLabel }}</label>
                <input class="input num" type="number" min="0" step="0.01" v-model.number="e.amount" placeholder="0.00" @input="markExtDirty(b.key)" />
              </div>
              <div class="field">
                <label>数量</label>
                <input class="input num" type="number" min="1" step="1" v-model.number="e.quantity" @input="markExtDirty(b.key)" />
              </div>
              <div class="field">
                <label>{{ b.partyLabel }}</label>
                <input class="input" v-model="e.counterparty" placeholder="选填" @input="markExtDirty(b.key)" />
              </div>
              <div class="field">
                <label>{{ b.dateLabel }}</label>
                <input class="input" type="date" v-model="e.time" @change="markExtDirty(b.key)" />
              </div>
              <div class="field">
                <label>货物来源</label>
                <select class="select" v-model="e.source" @change="markExtDirty(b.key)">
                  <option value="">未标注</option>
                  <option value="自采">自采</option>
                  <option value="回收">回收</option>
                  <option value="寄卖">寄卖</option>
                  <option value="闲鱼">闲鱼</option>
                </select>
              </div>
              <div class="field">
                <label>备注</label>
                <input class="input" v-model="e.note" placeholder="选填" @input="markExtDirty(b.key)" />
              </div>
            </div>
          </div>
          <div class="ext-foot muted">
            合计 <b class="num">{{ fmtMoney(b.total) }}</b> · 共 {{ b.items.length }} 笔
            <span class="ext-tip">{{ b.tip }}</span>
          </div>
        </div>
        <div v-else class="ext-empty muted">{{ b.empty }}</div>
      </div>

      <div class="cand-toolbar">
        <div class="search-wrap">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input class="input" v-model="q" @input="onSearchInput" placeholder="搜索商品 / 订单号 / 昵称；留空则按标题推荐相似" />
        </div>
        <select class="select" v-model="side" @change="loadCandidates">
          <option value="">买 + 卖</option><option value="buy">只看买单</option><option value="sell">只看卖单</option>
        </select>
        <button class="btn btn-sm" @click="autoMatch">一键匹配相似</button>
        <button class="btn btn-sm btn-ghost" @click="loadCandidates">刷新</button>
      </div>

      <div class="cand-meta muted">已选 <b>{{ selectedCount }}</b> 条 · 候选 {{ candidates.length }} 条 <span v-if="candidateLoading">· 加载中…</span>（可多选）</div>
      <div class="cand-list">
        <label v-for="c in candidates" :key="c.item.id" class="cand-item" :class="{ on: selectedMap[c.item.id] }">
          <input type="checkbox" :checked="!!selectedMap[c.item.id]" @change="toggleSelect(c.item.id)" />
          <span class="badge mini" :class="roleClass(c.item)">{{ roleOf(c.item) }}</span>
          <div class="cand-main">
            <div class="cand-title">{{ c.item.product || '未命名商品' }}</div>
            <!-- 候选只显示这条单据真正有的那一侧金额：买单不列卖出价、卖单不列买进价；
                 利润在挑选阶段没有意义（卖单的"利润"还没扣货品成本，买单的利润本来就是 0） -->
            <div class="cand-sub muted">
              <span>{{ shortTime(c.item.deal_time) }}</span>
              <template v-if="c.item.has_buy"><span> · 买 {{ fmtMoney(c.item.buy_price) }}</span></template>
              <template v-if="c.item.has_sell"><span> · 卖 {{ fmtMoney(c.item.sell_price) }}</span></template>
              <span v-if="counterpartyOf(c.item)"> · {{ counterpartyOf(c.item) }}</span>
            </div>
          </div>
          <span class="cand-score muted">{{ c.reason }}</span>
        </label>
        <div v-if="!candidates.length && !candidateLoading" class="empty empty-compact">
          <div class="empty-badge"><svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg></div>
          <div class="empty-title">没有候选记录</div>
          <div class="empty-sub">换个关键词，或先去录入截图</div>
        </div>
      </div>

      <div class="editor-foot">
        <button class="btn btn-quiet" @click="closeEditor">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="save">{{ saving ? '保存中…' : (editingId ? '保存修改' : '创建联单') }}</button>
      </div>
    </section>

    <!-- 联单列表 -->
    <div v-if="sortedGroups.length" class="group-grid">
      <section v-for="g in sortedGroups" :key="g.id" class="card group-card" :class="{ focused: focusedId === g.id, settled: isSettled(g) }" :data-group-id="g.id" :aria-label="isSettled(g) ? '已完结联单：' + g.title : g.title">
        <!-- 完结状态：整卡唯一一枚大印章水印，叠在内容之上 -->
        <div v-if="isSettled(g)" class="gc-stamp" aria-hidden="true">
          <div class="gc-stamp-inner">
            <span class="gc-stamp-star">★ ★ ★</span>
            <span class="gc-stamp-main">已完结</span>
            <span class="gc-stamp-sub">{{ stampDate(g.settled_at) }}</span>
          </div>
        </div>
        <div class="gc-head">
          <div class="gc-title">
            <span v-if="groupSource(g)" class="src-tag" :class="'src-' + groupSource(g)">{{ groupSource(g) }}</span>
            {{ g.title }}
          </div>
          <span class="gc-count muted">{{ g.stats.buy_count }} 买 / {{ g.stats.sell_count }} 卖</span>
        </div>
        <div class="gc-profit-box" :class="g.stats.profit >= 0 ? 'pos' : 'neg'">
          <div class="gc-profit-label">
            <span>净利润</span>
            <span class="gc-profit-tag">利润率 {{ (g.stats.profit_rate || 0).toFixed(1) }}%</span>
          </div>
          <div class="gc-profit num">{{ fmtMoney(g.stats.profit) }}</div>
          <!-- 销售额在下方 gc-metrics 第一格已有，这里不再重复（手机上尤其占地方） -->
          <div class="gc-profit-hint">共 {{ g.stats.buy_count + g.stats.sell_count }} 笔买卖</div>
        </div>
        <div class="gc-metrics">
          <div><span>销售额</span><b class="num">{{ fmtMoney(g.stats.sales) }}</b></div>
          <div><span>货品成本</span><b class="num">{{ fmtMoney(g.stats.goods) }}</b></div>
          <div><span>运费</span><b class="num">{{ fmtMoney(g.stats.shipping) }}</b></div>
          <!-- 服务费只显示软件服务费：无忧卖单独一行，否则两行加起来会重复计算 -->
          <div><span>软件服务费</span><b class="num">{{ fmtMoney((g.stats.fee || 0) - (g.stats.wuyou_fee || 0)) }}</b></div>
          <div><span>无忧卖</span><b class="num">{{ fmtMoney(g.stats.wuyou_fee) }}</b></div>
          <div><span>在库占用</span><b class="num">{{ fmtMoney(g.stats.unsold_cost) }}</b></div>
        </div>
        <div v-if="g.note" class="gc-note muted">{{ g.note }}</div>
        <div class="gc-items">
          <div v-for="row in (expandedId === g.id ? groupRows(g) : groupRows(g).slice(0, 2))" :key="row.key"
               class="gc-item" :class="{ 'is-sealed': isSettled(g), 'gc-item-ext': row.external, 'is-clickable': !row.external }"
               @click="!row.external && emit('view', row.item)"
               :title="row.external ? '只存在于本联单，不计入明细' : '点击查看这笔订单的详情'">
            <span class="badge mini" :class="row.kindClass">{{ row.kindLabel }}</span>
            <span v-if="row.source" class="src-dot" :class="'src-' + row.source" :title="'来源：' + row.source">{{ row.source }}</span>
            <span class="cell-ellipsis" :title="row.external ? '只存在于本联单，不计入明细' : row.product">{{ row.product || '未命名' }}</span>
            <b class="num" :class="{ muted: row.muted }">{{ fmtMoney(row.amount) }}</b>
          </div>
          <button v-if="groupRows(g).length > 2" class="link gc-more" :class="{ open: expandedId === g.id }" @click="expandedId = expandedId === g.id ? null : g.id">
            {{ expandedId === g.id ? '收起' : '还有 ' + (groupRows(g).length - 2) + ' 条，展开全部' }}
            <svg class="chev" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
          </button>
        </div>
        <div class="action-row gc-actions">
          <button class="action-primary" @click="toggleSettle(g)">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12l4.5 4.5L19 7"/></svg>
            {{ isSettled(g) ? '重新开启' : '完结' }}
          </button>
          <div class="action-icons">
            <button class="icon-btn" :disabled="isSettled(g)" @click="openEdit(g)" aria-label="编辑"
                    :title="isSettled(g) ? '已完结联单，请先重新开启再修改' : '编辑'">
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>
            </button>
            <button class="icon-btn icon-btn-danger" :disabled="isSettled(g)" @click="del(g)" aria-label="删除"
                    :title="isSettled(g) ? '已完结联单，请先重新开启再删除' : '删除'">
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/><path d="M10 11v6M14 11v6"/></svg>
            </button>
          </div>
        </div>
      </section>
    </div>
    <div v-else-if="!loading" class="card empty">
      <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/></svg></div>
      <div class="empty-title">还没有联动单</div>
      <div class="empty-sub">把同一批货的买单和卖单挂在一起，就能看到这批货到底赚了多少</div>
      <button class="btn btn-primary" @click="openCreate">创建第一个联单</button>
    </div>
    <div v-else class="group-grid">
      <div v-for="i in 4" :key="i" class="card skel-card">
        <span class="skeleton skel-line w70"></span>
        <span class="skeleton skel-value"></span>
        <div class="skel-metrics"><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span></div>
      </div>
    </div>
  </div>
</template>
