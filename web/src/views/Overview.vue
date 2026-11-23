<script setup>
import { ref, computed, watch, nextTick, onMounted, onActivated, onUnmounted } from 'vue'
import { api } from '../api'
import { fmtMoney, fmtNum, shortTime } from '../utils'
import AnimatedNumber from '../components/AnimatedNumber.vue'
import HelpTip from '../components/HelpTip.vue'
import InsightsPanel from '../components/InsightsPanel.vue'
import { allShopsMode } from '../shopScope'
import ProductProfit from '../components/ProductProfit.vue'
import AdvicePanel from '../components/AdvicePanel.vue'

const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['add', 'goto-records', 'goto-record', 'goto-product', 'goto-settings', 'goto-calc', 'goto-report', 'notify'])

// 备份提醒：超过 7 天没备份（或从未备份）就在首页提一句
const backupLast = ref(null) // null：尚未读取或读取失败；空字符串：确实没有备份
const monthTarget = ref(0)   // 本月利润目标，0=未设置
const targetForMonth = ref('')
function currentMonth() {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}
// 完结单核算默认收起：手机首屏放不下完整卡片，收起后只占一行，
// 首屏不再出现「被导航栏切掉一半」的尴尬；点标题展开看三个金额。
const ssOpen = ref(true)
const backupWarn = computed(() => {
  if (backupLast.value === null) return ''
  if (!backupLast.value) return '还没有备份过，数据只存在这台机器上'
  const d = new Date(String(backupLast.value).replace(/-/g, '/'))
  if (isNaN(d.getTime())) return ''
  const days = Math.floor((Date.now() - d.getTime()) / 86400000)
  if (days <= 7) return ''
  return `上次备份是 ${days} 天前（${backupLast.value}），建议现在备份一次`
})

// 销售额一张卡三种口径，切换看：总 / 今日 / 无成本。
// 不做成三张卡，是为了不让首页被 KPI 堆满。
const salesTab = ref('total')
const salesValue = computed(() => {
  if (salesTab.value === 'week') return stats.value?.week_sales || 0
  if (salesTab.value === 'month') return stats.value?.month_sales || 0
  return stats.value?.total_sales || 0
})
// 库龄分档：一眼看出压得最久的那部分钱
const ageTip = computed(() => {
  const s = stats.value || {}
  const f = (v) => fmtMoney(v || 0)
  return `在库按进货天数分档：\n30 天内 ${f(s.stock_30)}\n30–60 天 ${f(s.stock_60)}\n60–90 天 ${f(s.stock_90)}\n90 天以上 ${f(s.stock_old)}`
})
const salesFoot = computed(() => {
  if (salesTab.value === 'week') return '本周卖单成交价合计'
  if (salesTab.value === 'month') return '本月卖单成交价合计'
  return '所有登记卖单，按成交价计，未扣服务费'
})

const allShops = allShopsMode
const stats = ref(null)
const period = ref('month')
const periods = [{ v: 'month', t: '月' }, { v: 'quarter', t: '季' }, { v: 'week', t: '周' }]
const recent = ref([])
const trend = ref([])
const monthlyTrend = ref(null)
const loading = ref(true)
// 已经有数据时刷新不再回骨架屏：后台静默拉新，页面内容不闪
const showSkeleton = computed(() => loading.value && !stats.value)
const firstUse = computed(() => !!stats.value && Number(stats.value.total_count || 0) === 0)
const error = ref('')

// 图形入场：首次渲染就带着动画（滚到这儿的人能直接看到），
// 等「成本构成 / 月度趋势」这块第一次真正滚进视野时再放一次。
// 重放用「换一个同内容但不同名的动画」而不是摘类名再加回去——
// 后者依赖强制回流，Chrome 有时不认，缩放会一直停在终态，动画看起来没生效。
const animTick = ref(0)
const twoCol = ref(null)
let replayed = false

function replayCharts() {
  if (replayed || !stats.value) return
  replayed = true
  animTick.value++     // 类名切换 -> 浏览器当成一组全新动画，从 0 重放
}
function inViewport(el) {
  if (!el) return false
  const r = el.getBoundingClientRect()
  // 只露出顶边一条不算「看到了」，否则动画在首屏就放完，往下滚只剩静态图
  return r.top < window.innerHeight * 0.7 && r.bottom > 0
}
// 数据到位之后再挂观察器：加载态那会儿这块是骨架屏，高度和真实布局差很多，
// 提前挂会误判成「已经看见了」，动画就白白在首屏放掉。
watch(() => !!stats.value, async (ok) => {
  if (!ok) return
  // 等 DOM 换成真实数据再量位置：加载态是骨架屏，页面比真实布局短，
  // 这时候量出来的 top 偏小，会被误判成「已经在视野里」。
  await nextTick()
  if (inViewport(twoCol.value)) { replayCharts(); return }
  if (!twoCol.value || typeof IntersectionObserver === 'undefined') { replayCharts(); return }
  const io = new IntersectionObserver((es) => {
    if (!es.some(e => e.isIntersecting)) return
    replayCharts()
    io.disconnect()
  }, { threshold: 0.3 })
  io.observe(twoCol.value)
}, { immediate: true })


const settledIds = ref(new Set())
let loadSeq = 0
let lastLoaded = 0
async function load() {
  // 同明细页：并发刷新只认最后一次，过期的响应不再写回。
  const seq = ++loadSeq
  lastLoaded = Date.now()
  loading.value = true; error.value = ''
  try {
    const scope = allShops.value ? { all: '1' } : {}
    const chartPeriod = period.value
    const [s, l, t, gs, mt] = await Promise.all([
      api.stats(scope), api.list({ page: 1, page_size: 6, sort: 'created', order: 'desc' }),
      api.trend({ period: chartPeriod, ...scope }),
      allShops.value ? Promise.resolve(null) : api.groupsBrief().catch(() => null),
      chartPeriod === 'month' ? null : api.trend({ period: 'month', ...scope }).catch(() => null)
    ])
    if (seq !== loadSeq) return
    stats.value = s; recent.value = l.items; trend.value = t.months || []
    monthlyTrend.value = chartPeriod === 'month' ? trend.value : (mt?.months ?? null)
    const set = new Set()
    for (const g of (gs?.items || [])) {
      if (g.settled_at) for (const id of (g.item_ids || [])) set.add(id)
    }
    settledIds.value = set
  } catch (e) { if (seq === loadSeq) error.value = e.message || '加载失败' }
  finally { if (seq === loadSeq) loading.value = false }
}
onMounted(load)
// 从别的页面切回来：内容立刻可见，再静默拉一次最新（800ms 内刚拉过就跳过）
onActivated(() => { if (Date.now() - lastLoaded > 800) load() })
// 月底目标提醒：剩 3 天内且没达标，提示一次（按月份去重，会话内不再弹）
setTimeout(() => {
  try {
    if (!showTarget.value) return
    const t = monthTarget.value
    const d = daysLeft.value
    if (d > 3 || d < 1) return
    const p = thisMonth.value.profit || 0
    if (p >= t) return
    const key = 'xy-target-nudge-' + targetForMonth.value
    if (sessionStorage.getItem(key)) return
    sessionStorage.setItem(key, '1')
    emit('notify', `本月目标还差 ${fmtMoney(t - p)}，剩 ${d} 天`, false, null, { tone: 'warn' })
  } catch (e) { /* ignore */ }
}, 1500)
// 备份状态：只取「上次备份时间」，用来在首页提醒。
// 读取失败保持 null（未知），不要当成空字符串（从未备份）去催用户。
async function loadBackupStatus() {
  try { backupLast.value = (await api.getBackups()).last || '' } catch { backupLast.value = null }
}
onMounted(loadBackupStatus)
// 月度目标与趋势选择的周、月、季周期无关。
let targetSeq = 0
async function loadTarget() {
  const seq = ++targetSeq
  if (allShops.value) return
  const m = currentMonth()
  try {
    const r = await api.getMonthlyTarget(m)
    if (seq !== targetSeq) return
    monthTarget.value = Number(r?.target) || 0
    targetForMonth.value = m
  } catch {
    if (seq === targetSeq) targetForMonth.value = ''
  }
}
onMounted(loadTarget)
onActivated(() => { if (targetForMonth.value !== currentMonth()) loadTarget() })
watch(() => props.refreshKey, loadTarget)
watch(() => props.refreshKey, load)
watch(allShops, () => { load(); loadTarget() })
watch(period, () => load())

// 最近记录沿用明细口径：买单 / 卖单（已完结联单内额外显示「完结单」徽标）；
// 买进价按单据类型取卖价或买价，第二行卖单看「到手价」、买单看「总成本」，不对比利润
const isSettled = (r) => settledIds.value.has(r.id)
const rowBuyPrice = (r) => r.kind === 'purchase' ? (r.buy_price || 0) : (r.sell_price || 0)
// 卖单：到手价 = 卖价 − 运费 − 服务费 − 无忧卖；买单没有到手价，看总成本（买价 + 买运费）
const rowSubLabel = (r) => (r.has_sell ? '到手' : '成本')
const rowSubValue = (r) => (r.has_sell
  ? ((r.sell_price || 0) - (r.sell_shipping || 0) - (r.sell_fee || 0) - (r.sell_wuyou_fee || 0))
  : (r.cost || 0))

// 成本构成：四项之和 = stats.total_cost（完全成本口径）
// 销售额按成交总额（未扣费）计，服务费只作为成本项扣一次，利润不重复扣减。
const costParts = computed(() => {
  const s = stats.value || {}
  const wuyou = s.total_wuyou_fee || 0
  const feeOnly = Math.max(0, (s.total_fee || 0) - wuyou)
  return [
    { label: '货品成本', value: s.total_goods || 0, cls: 'c-goods', group: '已售成本构成' },
    { label: '运费支出', value: s.total_shipping || 0, cls: 'c-ship', group: '已售成本构成', hint: '已计入成本的那部分运费' },
    { label: '软件服务费', value: feeOnly, cls: 'c-fee', group: '已售成本构成' },
    { label: '无忧卖服务费', value: wuyou, cls: 'c-wuyou', group: '已售成本构成', hint: '无忧卖类目软件服务费' }
  ]
})
const costMax = computed(() => Math.max(1, ...costParts.value.map(p => p.value)))
const profitMargin = computed(() => stats.value?.profit_margin || 0)
const profitMarginLabel = computed(() =>
  Number(stats.value?.total_sales || 0) > 0 ? profitMargin.value.toFixed(1) + '%' : '—')

// 占比环形图：净利润 + 四项成本，一眼看结构占比
const donut = computed(() => {
  const s = stats.value || {}
  const cp = costParts.value
  const get = (label) => cp.find(p => p.label === label)?.value || 0
  const profit = s.total_profit || 0
  // raw 用于图例展示（保留正负号），value 取绝对值参与环形占比：
  // 亏损时也会以「净亏损」红段呈现，而不是被截成 0 只剩成本环
  const parts = [
    { label: profit >= 0 ? '净利润' : '净亏损', raw: profit, value: Math.abs(profit), cls: profit >= 0 ? 'c-profit' : 'c-loss', color: profit >= 0 ? 'var(--up)' : 'var(--down)' },
    { label: '货品成本', raw: get('货品成本'), value: Math.abs(get('货品成本')), cls: 'c-goods', color: 'var(--donut-goods)' },
    { label: '运费支出', raw: get('运费支出'), value: Math.abs(get('运费支出')), cls: 'c-ship', color: 'var(--chart-cost)' },
    { label: '软件服务费', raw: get('软件服务费'), value: Math.abs(get('软件服务费')), cls: 'c-fee', color: 'var(--donut-fee)' },
    { label: '无忧卖', raw: get('无忧卖服务费'), value: Math.abs(get('无忧卖服务费')), cls: 'c-wuyou', color: 'var(--donut-wuyou)' }
  ]
  const total = parts.reduce((a, p) => a + p.value, 0) || 1
  const C = 2 * Math.PI * 48
  let acc = 0
  const segs = parts.map(p => {
    const len = p.value / total * C
    const seg = { color: p.color, dash: `${len} ${C - len}`, offset: -acc, label: p.label, raw: p.raw, pct: p.value / total * 100 }
    acc += len
    return seg
  })
  const legend = parts.map(p => {
    const pct = p.value / total * 100
    return {
      label: p.label, cls: p.cls, value: p.raw,
      pct: pct > 0 && pct < 1 ? '<1' : String(Math.round(pct))
    }
  })
  return { profit, segs, legend }
})

// 环形图悬停 / 点选某一段：其余变淡，中心数字切换成这一项
const donutActive = ref(-1)
const donutCenter = computed(() => {
  const d = donut.value
  const i = donutActive.value
  if (i >= 0 && d.segs[i]) {
    const s = d.segs[i]
    return { label: s.label, value: fmtMoney(s.raw), sub: (s.pct > 0 && s.pct < 1 ? '<1' : Math.round(s.pct)) + '%', neg: s.raw < 0 }
  }
  return { label: '累计净利', value: fmtMoney(d.profit), sub: '利润率 ' + profitMarginLabel.value, neg: d.profit < 0 }
})

// Hero 展示的是「数据里最近有成交的那个月」：月初还没开单时，
// 它仍然停在上个月的真实数字，不会突然变成一条空记录。
const month = computed(() => {
  // 未填成交时间的记录只保留在累计口径里，不能冒充“最近成交月”。
  const ms = (monthlyTrend.value || []).filter(m => m?.month)
  return ms.length ? ms[ms.length - 1] : null
})
// 月度目标是按自然月存的，进度必须用「当前这个月」算，
// 不能跟着 Hero 的最近成交月走，否则月初会把上个月的利润算进本月进度。
const thisMonth = computed(() => {
  const m = currentMonth()
  return monthlyTrend.value?.find(x => x?.month === m) || { month: m, profit: 0 }
})
const showTarget = computed(() => !allShops.value && monthlyTrend.value !== null &&
  targetForMonth.value === thisMonth.value.month && monthTarget.value > 0)
const monthLabel = computed(() => {
  const m = month.value?.month || ''
  const p = m.split('-')
  return p.length === 2 ? `${p[0]} 年 ${parseInt(p[1], 10)} 月` : '本月'
})
const targetPct = computed(() => {
  if (!monthTarget.value || monthTarget.value <= 0) return 0
  return Math.max(0, Math.min(100, ((thisMonth.value.profit || 0) / monthTarget.value) * 100))
})
const daysLeft = computed(() => {
  const [y, mo] = currentMonth().split('-').map(Number)
  const last = new Date(y, mo, 0).getDate()
  const today = new Date()
  return Math.max(0, last - today.getDate() + 1)
})
const settledMargin = computed(() => {
  const s = stats.value || {}
  return s.settled_sales > 0 ? (s.settled_profit / s.settled_sales * 100) : 0
})
const settledAvg = computed(() => {
  const s = stats.value || {}
  return s.settled_sold > 0 ? (s.settled_profit / s.settled_sold) : 0
})

// 累计净利润 KPI 卡的本月净利润 + 环比/同比（取自月度趋势）
const monthDelta = computed(() => {
  const ms = monthlyTrend.value
  const latest = month.value
  if (!ms || !latest) return null
  const [year, mon] = latest.month.split('-').map(Number)
  const prevMonth = `${mon === 1 ? year - 1 : year}-${String(mon === 1 ? 12 : mon - 1).padStart(2, '0')}`
  const prev = ms.find(x => x?.month === prevMonth)
  const yearAgo = ms.find(x => x?.month === `${year - 1}-${String(mon).padStart(2, '0')}`)
  const pct = (cur, base) => {
    if (base === 0) return cur === 0 ? 0 : (cur > 0 ? 100 : -100)
    return (cur - base) / Math.abs(base) * 100
  }
  return {
    latest,
    mom: prev ? pct(latest.profit, prev.profit) : null,
    yoy: yearAgo ? pct(latest.profit, yearAgo.profit) : null
  }
})

// 窄屏只显示最近 6 个月，并收窄 viewBox，避免 SVG 缩放把 10px 标签压到不可读
const narrow = ref(false)
function syncNarrow() { narrow.value = window.innerWidth < 640 }
onMounted(() => {
  syncNarrow()
  window.addEventListener('resize', syncNarrow, { passive: true })
})
onUnmounted(() => window.removeEventListener('resize', syncNarrow))

const hoverIdx = ref(-1)
// 刻度要能表达 1.5k / 2.5k 这类值，直接 toFixed(0) 会把 2500 写成「3k」
const axisLabel = (v) => {
  const a = Math.abs(v)
  if (a >= 10000) {
    const w = v / 10000
    return (Number.isInteger(w) ? w : w.toFixed(1)) + '万'
  }
  if (a >= 1000) {
    const k = v / 1000
    return (Number.isInteger(k) ? k : k.toFixed(1)) + 'k'
  }
  return String(Math.round(v))
}

// 趋势图：销售额/成本双柱 + 利润折线。
// Y 轴建立含负区间的映射，净利润为负时折线落在 0 基线下方（跌绿），不再贴底。
const chart = computed(() => {
  const all = trend.value.filter(m => m.month)
  const ms = all.slice(-(narrow.value ? 6 : 12))
  if (!ms.length) return null
  const W = narrow.value ? 360 : 640
  const H = narrow.value ? 184 : 196
  const padL = narrow.value ? 38 : 46
  const padR = 8
  const padT = 14
  const padB = 26
  const plotH = H - padT - padB
  const plotW = W - padL - padR
  const profits = ms.map(m => Number(m.profit) || 0)
  // 值域收拢到「整」刻度：直接用数据极值会出现 934 这种零碎刻度，
  // 读图时反而分心。步长取 1/2/2.5/5/10 的整数倍，正好 4 格。
  const rawMax = Math.max(1, ...ms.map(m => Math.max(m.sales, m.cost)), ...profits)
  const rawMin = Math.min(0, ...profits)
  const niceStep = (x) => {
    const mag = Math.pow(10, Math.floor(Math.log10(Math.abs(x) || 1)))
    const f = Math.abs(x) / mag
    return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * mag
  }
  const tickStep = niceStep((rawMax - rawMin) / 4 || 1)
  let minV = Math.floor(rawMin / tickStep) * tickStep
  let maxV = minV + tickStep * 4
  while (maxV < rawMax) { minV += tickStep; maxV += tickStep }
  const span = (maxV - minV) || 1
  const y = v => padT + (maxV - v) / span * plotH
  const zeroY = y(0)
  const slot = plotW / ms.length
  const barW = Math.min(narrow.value ? 12 : 20, slot * 0.28)
  const bars = ms.map((m, i) => {
    const cx = padL + slot * i + slot / 2
    return {
      x: cx, label: m.month.slice(5) + '月',
      salesY: y(m.sales), salesH: Math.max(1, zeroY - y(m.sales)),
      costY: y(m.cost), costH: Math.max(1, zeroY - y(m.cost)),
      profitY: y(Number(m.profit) || 0),
      sales: m.sales, cost: m.cost, profit: Number(m.profit) || 0
    }
  })
  const line = bars.map((b, i) => `${i ? 'L' : 'M'}${b.x.toFixed(1)},${b.profitY.toFixed(1)}`).join(' ')
  const ticks = Array.from({ length: 5 }, (_, i) => {
    const v = maxV - span * i / 4
    return { v, y: y(v), label: axisLabel(v), zero: Math.abs(v) < 0.005 }
  })
  return { W, H, padL, padR, padT, padB, plotH, plotW, slot, barW, bars, line, zeroY, ticks }
})
</script>

<template>
  <div class="page ov-page">
    <div v-if="error" class="alert-err">{{ error }} <button class="btn btn-sm" @click="load">重试</button></div>

    <section v-if="firstUse" class="card ov-onboarding">
      <div class="ov-onboarding-copy">
        <p class="ov-onboarding-eyebrow">自托管闲鱼经营账本</p>
        <h2>每一单到底赚多少，<br>打开就算清。</h2>
        <p>上传订单截图或配置闲鱼同步，把成交价、进货成本、运费和平台服务费放在一张账单里。默认 OCR 在浏览器本地运行，图片不上传。</p>
        <div class="ov-onboarding-actions">
          <button class="btn btn-primary" @click="emit('add')">录入第一笔成交</button>
          <button class="btn btn-quiet" @click="emit('goto-settings')">配置闲鱼自动同步</button>
        </div>
      </div>
      <ol class="ov-onboarding-steps" aria-label="开始使用三步">
        <li><b>01</b><span>上传截图或同步闲鱼订单</span></li>
        <li><b>02</b><span>核对成本、运费与服务费</span></li>
        <li><b>03</b><span>查看真实利润和联单收益</span></li>
      </ol>
    </section>

    <!-- Hero：本月经营。完结单核算已经在下面单独一条，这里放"这个月做得怎么样" -->
    <section v-if="!firstUse" class="card hero-card">
      <div class="hero-main">
        <div class="hero-label">
          本月净利润
          <HelpTip class="hero-formula-tip" width="320" text="净利润 = 本月销售额 − 已售成本（只算已卖出部分的货品成本 + 运费 + 服务费）。分摊与「无成本」等细节见下方「利润口径说明」。" />
          <span class="hero-tag">{{ monthLabel }}</span>
          <button class="hero-calc" @click="emit('goto-calc')" title="定价速算：按成本算售价，或按售价算利润">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="2.5" width="16" height="19" rx="2.5"/><line x1="8" y1="7" x2="16" y2="7"/><line x1="8" y1="12" x2="9" y2="12"/><line x1="12" y1="12" x2="13" y2="12"/><line x1="16" y1="12" x2="17" y2="12"/><line x1="8" y1="17" x2="9" y2="17"/><line x1="12" y1="17" x2="13" y2="17"/><line x1="16" y1="17" x2="17" y2="17"/></svg>
            算价
          </button>
        </div>
        <div class="hero-value num" :class="(month?.profit || 0) >= 0 ? 'pos' : 'neg'">
          <AnimatedNumber :value="month?.profit || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber>
        </div>
        <div class="hero-sub">
          本月销售额 <b class="num">{{ fmtMoney(month?.sales || 0) }}</b>
          <span class="hero-sep">−</span>
          已售成本 <b class="num">{{ fmtMoney(month?.cost || 0) }}</b>
        </div>
      </div>
      <div class="hero-side">
        <div class="hero-metric"><span class="muted">本月成交 </span><b class="num">{{ fmtNum(month?.count || 0) }} 单</b></div>
        <div class="hero-metric">
          <span class="muted">环比上月利润</span>
          <b class="num" :class="(monthDelta?.mom || 0) >= 0 ? 'pos' : 'neg'">
            {{ monthDelta?.mom == null ? '—' : (monthDelta.mom >= 0 ? '+' : '') + monthDelta.mom.toFixed(1) + '%' }}
          </b>
        </div>
        <!-- 没有去年的数据时整条隐藏：只留一个「—」看着像坏了 -->
        <div v-if="monthDelta?.yoy != null" class="hero-metric">
          <span class="muted">同比去年</span>
          <b class="num" :class="monthDelta.yoy >= 0 ? 'pos' : 'neg'">
            {{ (monthDelta.yoy >= 0 ? '+' : '') + monthDelta.yoy.toFixed(1) + '%' }}
          </b>
        </div>
      </div>
      <div v-if="showTarget" class="hero-target">
        <div class="ht-line">
          <span>本月目标 {{ fmtMoney(monthTarget) }}<template v-if="daysLeft"> · 剩 {{ daysLeft }} 天</template></span>
          <b class="num">{{ targetPct.toFixed(0) }}%</b>
        </div>
        <div class="ht-bar" role="progressbar" aria-label="本月利润目标完成进度" :aria-valuenow="Math.round(targetPct)"><i :style="{ width: targetPct + '%' }"></i></div>
        <div class="ht-foot">已完成 {{ fmtMoney(Math.max(0, thisMonth.profit || 0)) }} / {{ fmtMoney(monthTarget) }}</div>
      </div>
    </section>

    <details class="formula-note">
      <summary>利润口径说明 <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg></summary>
      <div>净利润 = 销售额 − 已售成本。<b>已售成本只算已经卖出去的那部分</b>：货品成本按卖单带走的单件成本（买价 + 买运费按件分摊）计，加上卖运费、软件服务费、无忧卖服务费；没卖出去的进货落在「在库成本」里，不参与利润计算。「在库成本」= 所有有效采购 − 已填写成本卖单带走的成本 − 联单分摊给已卖部分的成本；联单里还没卖掉的货仍保留在库存，联单分摊部分不会重复扣。「总投入」只算所有买单的买价 + 买运费，所以它等于「已售货品成本 + 在库成本」，不包含卖运费和服务费；「已售成本」则还要再加上这两项。未填成交时间的记录只计入累计金额，不会冒充最近月份。「完结单核算」把两部分合起来算：点了完结的联单（成交额取联单内卖单的到手价，成本取联单内买价 + 买运费），以及自己填好成本的卖单（到手价 − 这单带走的货品成本）——填了成本就自动算已结算，不用再为了看一眼完结利润去包联单。明细卡片上的「到手价」= 成交价 − 卖运费 − 服务费 − 无忧卖，是真正落袋的钱，只作显示、不参与上面的计算，否则服务费会被扣两次。利润率 = 净利润 ÷ 销售额；卖单自己没填成本的，如果所在联单里有买进单，会按各卖单成交价占比把联单买单的买价 + 买运费分摊下来（只用于核算，不改单据）；既没填成本、又不在含买进单的联单里的，才按 0 计，可以去明细筛「未挂成本」补上；如果这单本来就没有货品成本（代卖 / 寄卖 / 赠送），在明细里标「无成本」即可，利润按销售额全额计。</div>
    </details>

    <!-- 四项核心指标 -->
    <div v-if="showSkeleton" class="kpi-grid">
      <div v-for="i in 4" :key="'k' + i" class="card kpi-card">
        <span class="skeleton kpi-skel-label"></span><span class="skeleton kpi-skel-value"></span>
      </div>
    </div>
    <div v-else class="kpi-grid kpi-grid-3">
      <!-- 经营：卖了多少、赚了多少 -->
      <div class="card kpi-card">
        <div class="kpi-label kpi-label-row">
          <span>销售额</span>
          <div class="kpi-seg">
            <button :class="{ on: salesTab === 'week' }" @click="salesTab = 'week'">周</button>
            <button :class="{ on: salesTab === 'month' }" @click="salesTab = 'month'">月</button>
            <button :class="{ on: salesTab === 'total' }" @click="salesTab = 'total'">总</button>
          </div>
        </div>
        <div class="kpi-value num"><AnimatedNumber :value="salesValue" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div>
        <div class="kpi-foot kpi-note">{{ salesFoot }}</div>
      </div>
      <div class="card kpi-card kpi-hl">
        <div class="kpi-label">累计净利润 <span class="kpi-tag">已成交</span></div>
        <div class="kpi-value num" :class="(stats?.total_profit || 0) >= 0 ? 'pos' : 'neg'"><AnimatedNumber :value="stats?.total_profit || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div>
        <div class="kpi-foot">
          利润率 {{ profitMarginLabel }}
          <span v-if="monthDelta" class="kpi-month-extra">
            · 本月净利 <b class="num">{{ fmtMoney(monthDelta.latest.profit) }}</b>
          </span>
        </div>
      </div>
      <div class="card kpi-card">
        <div class="kpi-label">已售成本 <span class="kpi-tag">已成交</span><HelpTip text="只算已经卖出去的那部分：货品成本按卖单带走的单件成本（买价+买运费分摊）计。还没卖出去的进货进「在库成本」，不参与利润计算。" /></div>
        <div class="kpi-value num"><AnimatedNumber :value="stats?.total_cost || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div>
        <div class="kpi-foot kpi-note">卖单货品成本 + 卖运费 + 服务费</div>
      </div>

      <!-- 库存：压了多少钱、卖了多少单 -->
      <div class="card kpi-card">
        <div class="kpi-label">在库成本 <span class="kpi-tag">占用资金</span></div>
        <div class="kpi-value num" style="color:var(--blue)"><AnimatedNumber :value="stats?.holding_cost || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div>
        <div class="kpi-foot">
          {{ fmtNum(stats?.pending_count || 0) }} 批待售
          <HelpTip :text="ageTip" />
        </div>
      </div>
      <div class="card kpi-card">
        <div class="kpi-label">总投入<HelpTip text="历史上所有进货的买价 + 买运费。总投入 = 已售货品成本 + 在库成本，不包含卖运费和服务费，用来看「一共压过多少钱」。" /></div>
        <div class="kpi-value num"><AnimatedNumber :value="stats?.total_invest || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></div>
        <div class="kpi-foot kpi-note">所有买单（买价 + 买运费）合计</div>
      </div>
      <div class="card kpi-card">
        <div class="kpi-label">总销量 <span class="kpi-tag">成交单数</span></div>
        <div class="kpi-value num"><AnimatedNumber :value="stats?.sold_count || 0" v-slot="{ value }">{{ fmtNum(value) }}</AnimatedNumber></div>
        <div class="kpi-foot">平均每单利润 {{ fmtMoney(stats?.avg_profit || 0) }}</div>
      </div>
    </div>

    <!-- 完结单核算：联单维度的三个数 -->
    <div v-if="!showSkeleton" class="settle-strip card" :class="{ 'ss-open': ssOpen }">
      <div class="ss-title">
        <button type="button" class="ss-toggle" :aria-expanded="ssOpen" @click="ssOpen = !ssOpen">
          完结单核算
          <!-- 单数是次要信息，做成标题旁的小标签；三个金额并列更好对比，也不再出现 2×2 右下角空一格 -->
          <span class="ss-count">{{ fmtNum(stats?.settled_sold || 0) }} 单</span>
          <!-- 默认收起：首屏空间有限，收起来能完整放下；点标题展开看金额 -->
          <svg class="ss-chev" :class="{ open: ssOpen }" aria-hidden="true" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>
        </button>
        <HelpTip text="两块钱合在一起算：① 点了「完结」的联单（完结成交额 = 联单内卖单到手价，完结成本 = 联单内买价 + 买运费）；② 卖单自己填好了成本的单（到手价 − 这单带走的货品成本），填完成本就自动算已结算，不用再包联单。两者之差就是完结净利润。" />
      </div>
      <div v-show="ssOpen" class="ss-nums">
        <div><span>完结成交额</span><b class="num"><AnimatedNumber :value="stats?.settled_sales || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></b></div>
        <div><span>完结成本</span><b class="num"><AnimatedNumber :value="stats?.settled_cost || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></b></div>
        <div><span>完结净利润</span><b class="num" :class="(stats?.settled_profit || 0) >= 0 ? 'pos' : 'neg'"><AnimatedNumber :value="stats?.settled_profit || 0" v-slot="{ value }">{{ fmtMoney(value) }}</AnimatedNumber></b></div>
      </div>
      <button class="btn btn-sm btn-quiet ss-report" @click="emit('goto-report')" title="生成可复制的周报 / 月报">生成报告</button>
    </div>

    <!-- 备份提醒 -->
    <div v-if="backupLast === null" class="backup-warn" role="status">
      <span>备份状态没读出来，暂时无法确认最近一次备份时间</span>
      <button class="btn btn-sm" @click="loadBackupStatus">重试</button>
    </div>
    <div v-else-if="backupWarn" class="backup-warn">
      <span>{{ backupWarn }}</span>
      <button class="btn btn-sm" @click="emit('goto-settings')">去备份</button>
    </div>

    <div ref="twoCol" class="two-col">
      <!-- 成本构成 + 占比环形图 -->
      <section class="card panel">
        <div class="panel-head"><h3>成本构成</h3><span class="muted">净利润与各项支出占比</span></div>
        <div class="cost-layout">
          <div class="donut-wrap">
            <svg class="donut-svg" :class="animTick % 2 ? 'donut-in-b' : 'donut-in'" viewBox="0 0 120 120" aria-hidden="true">
              <circle cx="60" cy="60" r="48" class="donut-bg" />
              <circle v-for="(s, i) in donut.segs" :key="i" cx="60" cy="60" r="48"
                class="donut-seg" :class="{ dim: donutActive >= 0 && donutActive !== i }"
                :stroke="s.color" :stroke-dasharray="s.dash" :stroke-dashoffset="s.offset"
                @mouseenter="donutActive = i" @mouseleave="donutActive = -1"
                @click="donutActive = donutActive === i ? -1 : i" />
            </svg>
            <div class="donut-center">
              <div class="donut-center-label">{{ donutCenter.label }}</div>
              <div class="donut-center-val num" :class="donutCenter.neg ? 'neg' : (donutActive < 0 ? 'pos' : '')">{{ donutCenter.value }}</div>
              <div class="donut-center-sub">{{ donutCenter.sub }}</div>
            </div>
          </div>
          <div class="cost-list">
            <div class="cost-row" v-for="s in donut.legend" :key="s.label">
              <div class="cost-name"><span class="cost-dot" :class="s.cls"></span><span>{{ s.label }}</span></div>
              <div class="cost-num num">{{ fmtMoney(s.value) }}</div>
              <div class="cost-pct">{{ s.pct }}%</div>
            </div>
          </div>
        </div>
        <div class="panel-foot muted">
          <span>已售成本 = 卖单货品成本 + 卖运费 + 软件服务费 + 无忧卖服务费；</span>
          <span class="pf-sales">只算已卖出的，没卖的进货在「在库成本」。</span>
        </div>
      </section>

      <!-- 月度趋势 -->
      <section class="card panel">
        <div class="panel-head">
          <h3>月度趋势</h3>
          <div class="period-chips">
            <button v-for="p in periods" :key="p.v" class="chip mini" :class="{ on: period === p.v }" @click="period = p.v">{{ p.t }}</button>
          </div>
        </div>
        <div v-if="chart" class="chart-wrap">
          <svg :key="period" :viewBox="`0 0 ${chart.W} ${chart.H}`" class="trend-svg chart-swap" :class="animTick % 2 ? 'chart-in-b' : 'chart-in'" preserveAspectRatio="xMidYMid meet">
            <!-- 网格与 Y 轴刻度 -->
            <g v-for="(t, i) in chart.ticks" :key="'t' + i">
              <line :x1="chart.padL" :y1="t.y" :x2="chart.W - chart.padR" :y2="t.y"
                :stroke="t.zero ? 'var(--chart-zero)' : 'var(--chart-grid)'"
                :stroke-dasharray="t.zero ? '' : '3 4'" stroke-width="1" />
              <text :x="chart.padL - 7" :y="t.y + 3.5" text-anchor="end" class="chart-tick">{{ t.label }}</text>
            </g>
            <!-- 值域取整后 0 可能刚好不在刻度上，零线单独补一条，正负分区不至于看不清 -->
            <line v-if="!chart.ticks.some(t => t.zero)" :x1="chart.padL" :y1="chart.zeroY"
              :x2="chart.W - chart.padR" :y2="chart.zeroY" stroke="var(--chart-zero)" stroke-width="1" />
            <!-- 双柱：入场时从零线往上长，逐组错峰 -->
            <g v-for="(b, i) in chart.bars" :key="'b' + i">
              <rect class="chart-bar-sales" :x="b.x - chart.barW - 1" :y="b.salesY" :width="chart.barW" :height="b.salesH" rx="2" :style="{ animationDelay: (i * 34) + 'ms' }" />
              <rect class="chart-bar-cost" :x="b.x + 1" :y="b.costY" :width="chart.barW" :height="b.costH" rx="2" :style="{ animationDelay: (i * 34 + 16) + 'ms' }" />
              <text :x="b.x" :y="chart.H - 8" text-anchor="middle" class="chart-label">{{ b.label }}</text>
            </g>
            <!-- 利润折线：pathLength=100 把长度归一化，描边动画不用测量真实长度 -->
            <path :d="chart.line" class="chart-profit-line" pathLength="100" fill="none" stroke-width="2.2" stroke-linejoin="round" stroke-linecap="round" />
            <circle v-for="(b, i) in chart.bars" :key="'p' + i" :cx="b.x" :cy="b.profitY"
              :r="hoverIdx === i ? 5.5 : 3" class="chart-dot"
              :class="b.profit >= 0 ? 'chart-dot-pos' : 'chart-dot-neg'" />
            <!-- hover 命中区 -->
            <rect v-for="(b, i) in chart.bars" :key="'h' + i" class="chart-hit" pointer-events="all"
              :x="b.x - chart.slot / 2" :y="chart.padT" :width="chart.slot" :height="chart.plotH"
              fill="transparent"
              @mouseenter="hoverIdx = i" @mouseleave="hoverIdx = -1"
              @touchstart.passive="hoverIdx = hoverIdx === i ? -1 : i" />
            <line v-if="hoverIdx >= 0" :x1="chart.bars[hoverIdx].x" :y1="chart.padT"
              :x2="chart.bars[hoverIdx].x" :y2="chart.H - chart.padB"
              stroke="var(--chart-zero)" stroke-width="1" stroke-dasharray="3 3" />
          </svg>

          <div v-if="hoverIdx >= 0" class="chart-tip"
            :style="{ left: Math.min(86, Math.max(14, chart.bars[hoverIdx].x / chart.W * 100)) + '%' }">
            <div class="ct-month">{{ chart.bars[hoverIdx].label }}</div>
            <div class="ct-row"><i class="sw-sales"></i>销售额 <b class="num">{{ fmtMoney(chart.bars[hoverIdx].sales) }}</b></div>
            <div class="ct-row"><i class="sw-cost"></i>成本 <b class="num">{{ fmtMoney(chart.bars[hoverIdx].cost) }}</b></div>
            <div class="ct-row"><i :class="chart.bars[hoverIdx].profit >= 0 ? 'sw-profit' : 'sw-profit-neg'"></i>利润
              <b class="num" :class="chart.bars[hoverIdx].profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(chart.bars[hoverIdx].profit) }}</b>
            </div>
          </div>

          <div class="chart-legend">
            <span><i class="sw-sales"></i>销售额</span>
            <span><i class="sw-cost"></i>成本</span>
            <span><i class="sw-profit"></i>利润</span>
          </div>
        </div>
        <div v-else-if="showSkeleton" class="chart-skeleton">
          <span v-for="i in 7" :key="'cs' + i" class="skeleton" :style="{ height: (30 + i * 12) + 'px' }"></span>
        </div>
        <div v-else class="empty chart-empty">
          <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v18h18"/><path d="M7 15l4-5 3 3 5-7"/></svg></div>
          <div class="empty-title">暂无月度趋势</div>
          <div class="empty-sub">录入带交易时间的记录后，这里会显示最近 12 个月的走势</div>
        </div>
      </section>
    </div>

    <AdvicePanel :refreshKey="refreshKey"
                 @goto-product="(p) => emit('goto-product', p)"
                 @goto-records="(p) => emit('goto-records', p)" />

    <InsightsPanel :refreshKey="refreshKey" @goto-product="(p) => emit('goto-product', p)" />

    <ProductProfit :refreshKey="refreshKey" @open-product="(p) => emit('goto-product', p)" />

    <!-- 最近记录 -->
    <section class="card panel">
      <div class="panel-head">
        <h3>最近记录</h3>
        <span class="link" @click="emit('goto-records')">查看全部明细 <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6"/></svg></span>
      </div>
      <div v-if="recent.length" class="recent-list">
        <div v-for="r in recent" :key="r.id" class="recent-row">
          <span class="badge-stack">
            <span class="badge mini" :class="r.kind === 'purchase' ? 'badge-buy' : 'badge-sell'">{{ r.kind === 'purchase' ? '买单' : '卖单' }}</span>
            <span v-if="isSettled(r)" class="settled-dot" title="已完结联单内的订单">✓ 已完结</span>
          </span>
          <div class="recent-main">
            <button type="button" class="recent-title cell-ellipsis recent-link" @click="emit('goto-record', r.id)"
                    title="在明细里定位这条记录">{{ r.product || '未命名商品' }}</button>
            <div class="recent-meta muted">添加 {{ shortTime(r.created_at) }} <span v-if="r.buy_order_no || r.sell_order_no">· {{ r.sell_order_no || r.buy_order_no }}</span></div>
          </div>
          <div class="recent-money">
            <div class="num strong">{{ fmtMoney(rowBuyPrice(r)) }}</div>
            <div class="muted recent-sub">{{ rowSubLabel(r) }} {{ fmtMoney(rowSubValue(r)) }}</div>
          </div>
        </div>
      </div>
      <div v-else-if="!showSkeleton" class="empty">
        <div class="empty-badge"><svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8l-9-5-9 5 9 5 9-5z"/><path d="M3 8v8l9 5 9-5V8"/><path d="M12 13v8"/></svg></div>
        <div class="empty-title">还没有商品记录</div>
        <div class="empty-sub">上传一张买进或卖出截图开始记账</div>
        <button class="btn btn-primary" @click="emit('add')">上传截图录入</button>
      </div>
      <div v-else class="skel-list" aria-label="正在加载">
        <div v-for="i in 4" :key="i" class="skel-row">
          <span class="skeleton skel-block"></span>
          <div class="skel-lines"><span class="skeleton skel-line w70"></span><span class="skeleton skel-line w40"></span></div>
          <span class="skeleton skel-money"></span>
        </div>
      </div>
    </section>
  </div>
</template>
