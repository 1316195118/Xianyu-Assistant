<script setup>
/**
 * 周报 / 月报一键生成：按时间范围拉统计，拼成可复制的文字报告。
 * 复用现有 /api/stats（支持 from/to）与 /api/records，不新增后端逻辑。
 * 报告口径与系统统计完全一致（销售额、已售成本、净利润、运费、服务费、利润率）。
 */
import { ref, computed } from 'vue'
import { api } from '../api'
import { fmtMoney, fmtNum } from '../utils'
import { activeShop } from '../shopStore'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['close'])

const period = ref('month')   // week / month
const loading = ref(false)
const report = ref('')
const err = ref('')
const copied = ref(false)

const periodLabel = computed(() => period.value === 'week' ? '本周' : '本月')

function range() {
  const now = new Date()
  const fmt = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  if (period.value === 'week') {
    // 周一为一周起点
    const day = (now.getDay() + 6) % 7
    const start = new Date(now); start.setDate(now.getDate() - day)
    return { from: fmt(start), to: fmt(now) }
  }
  const start = new Date(now.getFullYear(), now.getMonth(), 1)
  return { from: fmt(start), to: fmt(now) }
}

async function generate() {
  loading.value = true; err.value = ''; report.value = ''; copied.value = false
  try {
    const r = range()
    const s = await api.stats(r)
    const sales = Number(s.total_sales) || 0
    const cost = Number(s.total_cost) || 0
    const profit = Number(s.total_profit) || 0
    const margin = sales > 0 ? (profit / sales * 100) : 0
    const lines = []
    lines.push(`【${periodLabel.value}经营小结】${r.from} ~ ${r.to}`)
    lines.push(`店铺：${activeShop.value?.name || '当前店铺'}`)
    lines.push('')
    lines.push(`销售额　${fmtMoney(sales)}`)
    lines.push(`已售成本 ${fmtMoney(cost)}`)
    lines.push(`净利润　 ${fmtMoney(profit)}（利润率 ${margin.toFixed(1)}%）`)
    lines.push(`　其中：运费 ${fmtMoney(s.total_shipping || 0)} · 服务费 ${fmtMoney(s.total_fee || 0)}`)
    lines.push('')
    lines.push(`成交 ${fmtNum(s.sold_count || 0)} 单 · 平均每单利润 ${fmtMoney(s.avg_profit || 0)}`)
    lines.push(`在库成本 ${fmtMoney(s.holding_cost || 0)} · 待售 ${fmtNum(s.pending_count || 0)} 件`)
    lines.push(`总投入　 ${fmtMoney(s.total_invest || 0)}`)
    if (s.settled_profit) lines.push(`完结单净利润 ${fmtMoney(s.settled_profit)}（已完结 ${fmtNum(s.settled_sold || 0)} 单）`)
    lines.push('')
    lines.push('口径：净利润 = 销售额 − 已售成本（货品成本+运费+服务费），未卖出的进货不计入。')
    report.value = lines.join('\n')
  } catch (e) {
    err.value = e.message || '生成失败'
  } finally { loading.value = false }
}

async function copy() {
  if (!report.value) return
  try {
    await navigator.clipboard.writeText(report.value)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch (e) {
    // 剪贴板不可用时退回选中文本
    err.value = '复制失败，请手动选中文本复制'
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="cp-mask" @click.self="emit('close')">
      <div class="report-panel" role="dialog" aria-modal="true" aria-label="周报月报">
        <div class="pc-head">
          <div><b>周报 / 月报</b><span class="muted">一键生成可复制的经营小结</span></div>
          <button class="btn btn-sm btn-quiet" aria-label="关闭" @click="emit('close')">✕</button>
        </div>
        <div class="rp-toolbar">
          <div class="seg seg-sm">
            <button :class="{ 'on-sell': period === 'week' }" @click="period = 'week'">本周</button>
            <button :class="{ 'on-sell': period === 'month' }" @click="period = 'month'">本月</button>
          </div>
          <button class="btn btn-sm btn-primary" :disabled="loading" @click="generate">
            <span v-if="loading" class="spin"></span>{{ loading ? '生成中' : '生成报告' }}
          </button>
          <button v-if="report" class="btn btn-sm" @click="copy">{{ copied ? '✓ 已复制' : '复制' }}</button>
        </div>
        <div v-if="err" class="alert-err">{{ err }}</div>
        <textarea v-if="report" class="rp-text" :value="report" readonly spellcheck="false"></textarea>
        <div v-else class="rp-empty muted">选好周期后点「生成报告」，会按当前店铺的数据整理出一段可直接发出去的文字。</div>
      </div>
    </div>
  </Teleport>
</template>
