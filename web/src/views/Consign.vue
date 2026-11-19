<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { api } from '../api'
import { confirm } from '../confirm'
import { fmtMoney, fmtNum } from '../utils'

// 寄卖账本：别人的货放在这卖（寄卖），或垫钱替别人买（代采），都按归属人分账、可结算。
// 这两类单都不计入首页利润 / 成本（后端按 source 排除），只在这里统计。
const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['notify'])

const data = ref(null)
const err = ref('')
const q = ref('')
const active = ref('')
const from = ref('')
const to = ref('')
const rangeKey = ref('month')
const tab = ref('sell')            // sell=寄卖（我该付给寄卖人） buy=代采（归属人该还我）
const settleOpen = ref(false)
const settleOwner = ref(null)
const settleAmount = ref(0)
const settleNote = ref('')
const settleBusy = ref(false)
const historyOpen = ref(false)
const historyOwner = ref('')
const historyKind = ref('sell')
const history = ref([])

let settingRange = false
const fmtDate = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`

function applyRange(key) {
  settingRange = true
  rangeKey.value = key
  const now = new Date()
  if (key === 'month') {
    from.value = fmtDate(new Date(now.getFullYear(), now.getMonth(), 1))
    to.value = fmtDate(now)
  } else if (key === 'lastMonth') {
    const s = new Date(now.getFullYear(), now.getMonth() - 1, 1)
    const e = new Date(now.getFullYear(), now.getMonth(), 0)
    from.value = fmtDate(s); to.value = fmtDate(e)
  } else if (key === 'quarter') {
    const qm = Math.floor(now.getMonth() / 3) * 3
    from.value = fmtDate(new Date(now.getFullYear(), qm, 1))
    to.value = fmtDate(now)
  } else if (key === 'year') {
    from.value = fmtDate(new Date(now.getFullYear(), 0, 1))
    to.value = fmtDate(now)
  } else {
    from.value = ''; to.value = ''
  }
  setTimeout(() => { settingRange = false }, 0)
}

async function load() {
  try {
    const params = {}
    if (from.value) params.from = from.value
    if (to.value) params.to = to.value
    data.value = await api.consignStats(params)
    err.value = ''
  } catch (e) {
    err.value = e.message || '加载失败'
  }
}
onMounted(() => { applyRange('month'); load() })
watch(() => props.refreshKey, load)
watch([from, to], (v, old) => {
  // 手改日期时取消预设高亮
  if (!settingRange && old && (v[0] !== old[0] || v[1] !== old[1])) rangeKey.value = 'custom'
  if (!settingRange) load()
})

const isBuy = computed(() => tab.value === 'buy')
const rawList = computed(() => {
  if (!data.value) return []
  return (isBuy.value ? data.value.buyers : data.value.owners) || []
})
const list = computed(() => {
  const k = q.value.trim()
  return k ? rawList.value.filter((o) => (o.owner || '').includes(k)) : rawList.value
})
const total = computed(() => (data.value && data.value.total) || {})
const shown = computed(() => (active.value ? list.value.filter((o) => o.owner === active.value) : list.value))

// 汇总卡：按当前账本换口径
const cards = computed(() => {
  const t = total.value
  if (isBuy.value) {
    return [
      { label: '区间代采垫付', value: fmtMoney(t.buy_amount || 0) },
      { label: '区间代采单数', value: fmtNum(t.buy_orders || 0), unit: '单' },
      { label: '累计待收', value: fmtMoney(t.buy_pending || 0), neg: (t.buy_pending || 0) > 0 },
      { label: '累计已收', value: fmtMoney(t.buy_settled || 0) }
    ]
  }
  return [
    { label: '区间寄卖销售额', value: fmtMoney(t.sales || 0) },
    { label: '区间寄卖单数', value: fmtNum(t.orders || 0), unit: '单' },
    { label: '累计待结', value: fmtMoney(t.pending || 0), neg: (t.pending || 0) > 0 },
    { label: '累计已结', value: fmtMoney(t.settled || 0) }
  ]
})

function switchTab(k) {
  if (tab.value === k) return
  tab.value = k
  active.value = ''
}
const dayOf = (t) => String(t || '').slice(0, 10) || '—'
const pickOwner = (n) => { active.value = active.value === n ? '' : n }

function openSettle(o) {
  settleOwner.value = o
  settleAmount.value = Number(o.pending) || 0
  settleNote.value = ''
  settleOpen.value = true
}
async function submitSettle() {
  if (!settleOwner.value || settleBusy.value) return
  const amount = Number(settleAmount.value) || 0
  if (amount <= 0) { emit('notify', '结算金额要大于 0', true); return }
  const kind = isBuy.value ? 'buy' : 'sell'
  const name = settleOwner.value.owner
  settleBusy.value = true
  try {
    await api.consignSettle({ owner: name, kind, amount, note: settleNote.value.trim() })
    emit('notify', isBuy.value ? `已收到「${name}」还款 ${fmtMoney(amount)}` : `已给「${name}」结算 ${fmtMoney(amount)}`)
    settleOpen.value = false
    await load()
  } catch (e) {
    emit('notify', '结算失败：' + e.message, true)
  } finally { settleBusy.value = false }
}

async function delSettlement(h) {
  const buy = historyKind.value === 'buy'
  const ok = await confirm({
    title: buy ? '删除收款记录' : '删除结算记录',
    message: buy ? '删除后这笔金额会重新计入待收。' : '删除后这笔金额会重新计入待结。',
    confirmText: '删除', danger: true
  })
  if (!ok) return
  try {
    await api.deleteConsignSettlement(h.id)
    emit('notify', '已删除结算记录')
    history.value = history.value.filter((x) => x.id !== h.id)
    await load()
  } catch (e) { emit('notify', '删除失败：' + e.message, true) }
}

async function openHistory(o) {
  historyOwner.value = o.owner
  historyKind.value = isBuy.value ? 'buy' : 'sell'
  history.value = []
  historyOpen.value = true
  try {
    const r = await api.consignSettlements({ owner: o.owner, kind: historyKind.value })
    history.value = r.items || []
  } catch (e) {
    emit('notify', '读取结算记录失败：' + e.message, true)
  }
}
</script>

<template>
  <div class="page">
    <div v-if="err" class="alert-err">{{ err }} <button class="btn btn-sm" @click="load">重试</button></div>

    <!-- 两本账：寄卖（别人的货我代卖） / 代采（垫钱替别人买） -->
    <div class="consign-tabs" role="tablist" aria-label="账本切换">
      <button class="ctab" :class="{ on: tab === 'sell' }" role="tab" :aria-selected="tab === 'sell'" @click="switchTab('sell')">
        <span class="ctab-t">寄卖</span>
        <span class="ctab-s">别人的货放我这卖</span>
      </button>
      <button class="ctab on-buy" :class="{ on: tab === 'buy' }" role="tab" :aria-selected="tab === 'buy'" @click="switchTab('buy')">
        <span class="ctab-t">代采</span>
        <span class="ctab-s">垫钱替别人买</span>
      </button>
    </div>

    <!-- 汇总：区间金额 + 累计结算 -->
    <div class="sum-grid consign-sums">
      <div v-for="c in cards" :key="c.label" class="card sum-card">
        <div class="m-label">{{ c.label }}</div>
        <div class="m-value num" :class="{ neg: c.neg }">{{ c.value }}<small v-if="c.unit">{{ c.unit }}</small></div>
      </div>
    </div>

    <!-- 查询条件 -->
    <section class="card consign-tools">
      <div class="chips consign-range">
        <button class="chip" :class="{ on: rangeKey === 'month' }" @click="applyRange('month')">本月</button>
        <button class="chip" :class="{ on: rangeKey === 'lastMonth' }" @click="applyRange('lastMonth')">上月</button>
        <button class="chip" :class="{ on: rangeKey === 'quarter' }" @click="applyRange('quarter')">本季度</button>
        <button class="chip" :class="{ on: rangeKey === 'year' }" @click="applyRange('year')">今年</button>
        <button class="chip" :class="{ on: rangeKey === 'all' }" @click="applyRange('all')">全部</button>
      </div>
      <div class="consign-dates">
        <input class="input date-input" type="date" v-model="from" aria-label="开始日期" />
        <span class="muted">至</span>
        <input class="input date-input" type="date" v-model="to" aria-label="结束日期" />
      </div>
      <div class="search-wrap">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input class="input" v-model="q" :placeholder="isBuy ? '搜委托人，如 李四' : '搜寄卖人，如 张三'" />
      </div>
      <span v-if="active" class="consign-active-chip" @click="active = ''">只看「{{ active }}」 ×</span>
    </section>

    <!-- 按人分账 -->
    <section v-for="o in shown" :key="o.owner" class="card consign-owner" :class="{ open: active === o.owner, buy: isBuy }">
      <button class="consign-owner-head" @click="pickOwner(o.owner)" :aria-expanded="active === o.owner">
        <span class="consign-avatar" aria-hidden="true">{{ (o.owner || '?')[0] }}</span>
        <span class="consign-owner-main">
          <b>{{ o.owner }}</b>
          <small v-if="isBuy">{{ o.orders }} 单 · 区间垫付 {{ fmtMoney(o.amount) }} · 待收 <i class="pending-num">{{ fmtMoney(o.pending) }}</i></small>
          <small v-else>{{ o.orders }} 单 · 区间销售 {{ fmtMoney(o.sales) }} · 待结 <i class="pending-num">{{ fmtMoney(o.pending) }}</i></small>
        </span>
        <span class="consign-owner-actions" @click.stop>
          <button class="btn btn-sm btn-primary" :disabled="o.pending <= 0" @click="openSettle(o)">{{ isBuy ? '收款' : '结算' }}</button>
          <button class="btn btn-sm btn-quiet" @click="openHistory(o)">记录</button>
        </span>
        <svg class="consign-chev" :class="{ up: active === o.owner }" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
      </button>

      <template v-if="active === o.owner">
        <template v-if="isBuy">
          <div class="consign-items-head buy">
            <span>商品</span><span>买价</span><span>运费</span><span>时间</span><span>卖家</span>
          </div>
          <div v-for="it in o.items" :key="it.id" class="consign-item buy">
            <span class="cell-ellipsis" :title="it.product">{{ it.product }}</span>
            <b class="num">{{ fmtMoney(it.buy_price) }}</b>
            <span class="muted num">{{ fmtMoney(it.buy_shipping) }}</span>
            <span class="muted">{{ dayOf(it.buy_time) }}</span>
            <span class="muted cell-ellipsis">{{ it.counterparty || '—' }}</span>
          </div>
        </template>
        <template v-else>
          <div class="consign-items-head">
            <span>商品</span><span>成交价</span><span>到手</span><span>费用</span><span>时间</span><span>买家</span>
          </div>
          <div v-for="it in o.items" :key="it.id" class="consign-item">
            <span class="cell-ellipsis" :title="it.product">{{ it.product }}</span>
            <b class="num">{{ fmtMoney(it.sell_price) }}</b>
            <b class="num net-col">{{ fmtMoney(it.sell_net) }}</b>
            <span class="muted num">{{ fmtMoney(it.sell_fee + it.sell_shipping) }}</span>
            <span class="muted">{{ dayOf(it.sell_time) }}</span>
            <span class="muted cell-ellipsis">{{ it.counterparty || '—' }}</span>
          </div>
        </template>
      </template>
      <div v-else-if="o.items.length" class="consign-owner-preview cell-ellipsis">
        {{ o.items.map((i) => i.product).join('、') }}
      </div>
    </section>

    <div v-if="!list.length && !err" class="empty">
      <div class="empty-badge">
        <svg v-if="isBuy" width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M6 2 3 6v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6l-3-4z"/><path d="M3 6h18"/><path d="M16 10a4 4 0 0 1-8 0"/></svg>
        <svg v-else width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.9"/><path d="M16 3.1a4 4 0 0 1 0 7.8"/></svg>
      </div>
      <div class="empty-title">{{ isBuy ? '这个区间没有代采单' : '这个区间没有寄卖单' }}</div>
      <div class="empty-sub" v-if="isBuy">换个日期范围看看，或录入买单时把「货物来源」选为「代采」并填上委托人，垫付金额会记在这里等他结算。</div>
      <div class="empty-sub" v-else>换个日期范围看看，或在录入/编辑卖单时把「货物来源」选为「寄卖」并填上归属人。</div>
    </div>

    <!-- 结算弹窗 -->
    <div v-if="settleOpen" class="modal-mask set-sheet" @click.self="settleOpen = false">
      <div class="modal" role="dialog" aria-modal="true" :aria-label="isBuy ? '代采收款' : '寄卖结算'">
        <div class="modal-head"><h3>{{ isBuy ? (settleOwner && settleOwner.owner) + ' 还款给我' : '结算给 ' + (settleOwner && settleOwner.owner) }}</h3><button class="x" @click="settleOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <p v-if="isBuy" class="muted set-sheet-desc">这个数是你替他垫付的货款加买入运费。默认填当前待收金额，可以按实际收到的改。收到后待收会相应减少，代采记录仍可按日期查。</p>
          <p v-else class="muted set-sheet-desc">默认填的是当前待结金额，可以改成实际结算的数目。结算后待结会相应减少，销售记录仍可按日期查。</p>
          <div class="field">
            <label>{{ isBuy ? '收到金额' : '结算金额' }}</label>
            <input class="input num" type="number" min="0.01" step="0.01" v-model.number="settleAmount" />
          </div>
          <div class="field">
            <label>备注（可选）</label>
            <input class="input" v-model="settleNote" placeholder="如：微信转账 / 现金" />
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn btn-ghost" @click="settleOpen = false">取消</button>
          <button class="btn btn-primary" :disabled="settleBusy" @click="submitSettle">
            <span v-if="settleBusy" class="spin"></span>{{ isBuy ? '确认收款' : '确认结算' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 结算记录 -->
    <div v-if="historyOpen" class="modal-mask set-sheet" @click.self="historyOpen = false">
      <div class="modal" role="dialog" aria-modal="true" aria-label="结算记录">
        <div class="modal-head"><h3>{{ historyOwner }} 的{{ historyKind === 'buy' ? '收款' : '结算' }}记录</h3><button class="x" @click="historyOpen = false" aria-label="关闭">×</button></div>
        <div class="modal-body">
          <div v-if="!history.length" class="muted set-sheet-desc">还没有{{ historyKind === 'buy' ? '收款' : '结算' }}过。</div>
          <div v-for="h in history" :key="h.id" class="settle-row">
            <span class="muted">{{ h.settled_at.slice(0, 16) }}</span>
            <b class="num">{{ fmtMoney(h.amount) }}</b>
            <span class="muted cell-ellipsis">{{ h.note || '—' }}</span>
            <button class="btn btn-sm btn-ghost btn-danger" @click="delSettlement(h)">删除</button>
          </div>
        </div>
        <div class="modal-foot"><button class="btn btn-primary" @click="historyOpen = false">完成</button></div>
      </div>
    </div>
  </div>
</template>
