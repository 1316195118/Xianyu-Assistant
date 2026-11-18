<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { api } from '../api'
import { fmtMoney } from '../utils'
import HelpTip from './HelpTip.vue'

// 经营建议：把「该动手做什么」直接摆出来，而不是只给一堆数字。
// 数据全部来自已有接口（统计 / 商品利润 / 洞察），这里只做规则判断与排序，
// 不新增服务端口径，避免同一件事出现两套算法。
const props = defineProps({ refreshKey: { type: Number, default: 0 } })
const emit = defineEmits(['goto-product', 'goto-records'])

const stats = ref(null)
const profitRows = ref([])
const extras = ref(null)
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const [s, p, e] = await Promise.all([
      api.stats(),
      api.productProfit(),
      api.statsExtras().catch(() => null)   // 洞察失败不影响主体建议
    ])
    stats.value = s
    profitRows.value = p.items || []
    extras.value = e
  } catch (err) {
    // 保留上一次结果：刷新失败时整块消失比显示旧数据更让人困惑
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => props.refreshKey, load)

const money = (v) => fmtMoney(Number(v) || 0)
const bump = (v) => Math.min(90, Math.max(0, Number(v) || 0))   // 金额/数量加成封顶
const pct = (v) => (Number(v) || 0).toFixed(1) + '%'

// 规则清单：每条都有唯一 key、优先级分、可选的跳转动作。
// 分数 = 严重度基数 + 小幅加成：金额不能直接当分数加（在库动辄上万，
// 会把「10 单没挂成本」这种更该先处理的事挤到后面），所以加成统一压到 90 以内。
const advices = computed(() => {
  const s = stats.value || {}
  const rows = (profitRows.value || []).filter(r => Number(r.sold) > 0)
  const ex = extras.value || {}
  const out = []

  // 1) 未挂成本：利润最不可信的一件事，永远排第一
  const unlinked = rows.reduce((n, r) => n + (Number(r.unlinked) || 0), 0)
  if (unlinked > 0) {
    out.push({
      key: 'unlinked', level: 'warn', icon: 'link', score: 1000 + bump(unlinked * 5),
      title: `${unlinked} 单卖单还没挂成本`,
      desc: '这些单的货品成本按 0 计，利润会虚高。挂上买进单后数字才准。',
      action: { label: '去挂成本', type: 'unlinked' }
    })
  }

  // 2) 亏损商品：亏得最多的点名，便于直接去改价或止损
  const losses = rows.filter(r => Number(r.profit) < 0)
  if (losses.length) {
    const worst = losses.slice().sort((a, b) => a.profit - b.profit)[0]
    const lossSum = losses.reduce((n, r) => n + Number(r.profit), 0)
    out.push({
      key: 'loss', level: 'warn', icon: 'down', score: 900 + bump(Math.abs(lossSum) / 100),
      title: `${losses.length} 个商品在亏钱，合计 ${money(lossSum)}`,
      desc: `亏最多的是「${worst.product}」（${money(worst.profit)}）。先看进价和运费，再看是不是定价压太低。`,
      action: { label: '看这个商品', product: { name: worst.product, norm: worst.norm } }
    })
  }

  // 3) 库龄：90 天以上的货占在库比例高，说明压货
  const oldStock = Number(s.stock_old) || 0
  const hold = Number(s.holding_cost) || 0
  if (oldStock > 0 && hold > 0 && oldStock / hold >= 0.3) {
    out.push({
      key: 'old', level: 'warn', icon: 'clock', score: 800 + bump(oldStock / 100),
      title: `90 天以上的货压了 ${money(oldStock)}`,
      desc: `占在库 ${money(hold)} 的 ${pct(oldStock / hold * 100)}。这类货越放越难卖，建议打包或降价走掉。`,
      action: null
    })
  }

  // 4) 滞销件数（阈值在设置里可改）
  const staleCount = Number(ex.stale_count) || 0
  if (staleCount > 0) {
    const items = ex.stale_items || []
    const maxDays = items.reduce((n, it) => Math.max(n, Number(it.days) || 0), 0)
    out.push({
      key: 'stale', level: 'tip', icon: 'clock', score: 700 + bump(staleCount * 5),
      title: `${staleCount} 件货压了超过 ${ex.stale_threshold || 60} 天`,
      desc: maxDays ? `最早的一件已经 ${maxDays} 天。越放越难卖，先降价或打包走掉，别等成死库存。` : '越放越难卖，先降价或打包走掉，别等成死库存。',
      action: null
    })
  }

  // 5) 现金流：在库占用过高，下一批货会吃紧
  const invest = Number(s.total_invest) || 0
  if (invest > 0 && hold / invest >= 0.6) {
    out.push({
      key: 'cash', level: 'tip', icon: 'wallet', score: 600 + bump(hold / 100),
      title: `在库占用了 ${pct(hold / invest * 100)} 的投入`,
      desc: `总投入 ${money(invest)}，还有 ${money(hold)} 压在货上。出货慢会挡住下一批收货。`,
      action: { label: '看在库', type: 'buy' }
    })
  }

  // 6) 服务费率：定价时容易被忽略的固定成本
  const sales = Number(s.total_sales) || 0
  const fee = Number(s.total_fee) || 0
  if (sales > 0 && fee / sales >= 0.03) {
    out.push({
      key: 'fee', level: 'tip', icon: 'fee', score: 500 + bump(fee / 100),
      title: `平台服务费占销售额 ${pct(fee / sales * 100)}`,
      desc: `合计 ${money(fee)}。定价时把这部分算进去，别让利润被费率吃掉。`,
      action: null
    })
  }

  // 7) 值得补货的款：卖过 2 单以上、利润率还不错
  const steady = rows.filter(r => Number(r.sold) >= 2 && Number(r.margin) >= 20)
    .sort((a, b) => b.profit - a.profit)[0]
  if (steady) {
    out.push({
      key: 'restock', level: 'good', icon: 'up', score: 400 + bump(steady.profit / 100),
      title: `「${steady.product}」是稳定赚钱的款`,
      desc: `卖了 ${steady.sold} 单，利润 ${money(steady.profit)}，利润率 ${pct(steady.margin)}。可以再收同款。`,
      action: { label: '看这个商品', product: { name: steady.product, norm: steady.norm } }
    })
  }

  // 8) 全部正常时给一个明确的「没事」
  if (!out.length && Number(s.sold_count) > 0) {
    out.push({
      key: 'ok', level: 'good', icon: 'check', score: 0,
      title: '暂时没有要处理的异常',
      desc: '成本、库龄和利润率都在正常范围，继续保持。',
      action: null
    })
  }

  return out.sort((a, b) => b.score - a.score).slice(0, 4)
})

function run(a) {
  if (!a || !a.action) return
  if (a.action.product) emit('goto-product', a.action.product)
  else if (a.action.type) emit('goto-records', { type: a.action.type })
}
</script>

<template>
  <section class="card panel advice-panel">
    <div class="panel-head">
      <h3>经营建议<HelpTip text="根据当前店铺的成本、库龄、利润与费率自动生成，按影响大小排序。只提示能直接动手的事，点右侧按钮会跳到对应明细。" /></h3>
      <span v-if="!loading && advices.length" class="muted">{{ advices.length }} 条</span>
    </div>

    <div v-if="loading" class="advice-list">
      <div v-for="i in 2" :key="'sk' + i" class="advice-item">
        <span class="skeleton skel-line w70"></span>
      </div>
    </div>

    <div v-else class="advice-list">
      <div v-for="(a, i) in advices" :key="a.key" class="advice-item" :class="'lv-' + a.level"
           :style="{ animationDelay: (i * 70) + 'ms' }">
        <span class="advice-ico">
          <svg v-if="a.icon === 'link'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1"/><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1"/></svg>
          <svg v-else-if="a.icon === 'down'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7l6 6 4-4 8 8"/><path d="M21 21h-6v-6"/></svg>
          <svg v-else-if="a.icon === 'clock'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>
          <svg v-else-if="a.icon === 'wallet'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v1"/><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-6a2 2 0 0 0-2-2H5"/><circle cx="16" cy="14" r="1.2"/></svg>
          <svg v-else-if="a.icon === 'fee'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 5L5 19"/><circle cx="7.5" cy="7.5" r="2.5"/><circle cx="16.5" cy="16.5" r="2.5"/></svg>
          <svg v-else-if="a.icon === 'up'" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 17l6-6 4 4 8-8"/><path d="M21 3h-6v6"/></svg>
          <svg v-else width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>
        </span>
        <div class="advice-main">
          <div class="advice-title">{{ a.title }}</div>
          <div class="advice-desc muted">{{ a.desc }}</div>
        </div>
        <button v-if="a.action" class="btn btn-sm btn-quiet advice-act" @click="run(a)">{{ a.action.label }}</button>
      </div>
      <div v-if="!stats" class="advice-empty muted">数据加载失败，<button class="link" @click="load">重试</button></div>
      <div v-else-if="!advices.length" class="advice-empty muted">录入几单之后，这里会按数据给出下一步建议。</div>
    </div>
  </section>
</template>
