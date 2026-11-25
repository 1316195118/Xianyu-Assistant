<script setup>
/**
 * 周报 / 月报一键生成：按时间范围拉统计，拼成可复制的文字报告。
 * 复用现有 /api/stats（支持 from/to）与 /api/records，不新增后端逻辑。
 * 报告口径与系统统计完全一致（销售额、已售成本、净利润、运费、服务费、利润率）。
 */
import { ref, computed, watch, onUnmounted } from 'vue'
import { api } from '../api'
import { fmtMoney, fmtNum } from '../utils'
import { activeShop } from '../shopStore'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['close'])

const period = ref('month')   // week / month
const loading = ref(false)
const report = ref('')
const reportData = ref(null)
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

const onEscKey = (e) => { if (e.key === 'Escape') emit('close') }
watch(() => props.open, (v) => {
  if (v) {
    document.addEventListener('keydown', onEscKey)
    if (!reportData.value && !loading.value) generate()
  } else {
    document.removeEventListener('keydown', onEscKey)
  }
})
watch(period, () => {
  report.value = ''
  reportData.value = null
  copied.value = false
  err.value = ''
})
onUnmounted(() => document.removeEventListener('keydown', onEscKey))

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
    const data = {
      periodLabel: periodLabel.value,
      from: r.from,
      to: r.to,
      shop: activeShop.value?.name || '当前店铺',
      sales,
      cost,
      profit,
      margin,
      shipping: Number(s.total_shipping) || 0,
      fee: Number(s.total_fee) || 0,
      soldCount: Number(s.sold_count) || 0,
      avgProfit: Number(s.avg_profit) || 0,
      holdingCost: Number(s.holding_cost) || 0,
      pendingCount: Number(s.pending_count) || 0,
      totalInvest: Number(s.total_invest) || 0,
      settledProfit: Number(s.settled_profit) || 0,
      settledSold: Number(s.settled_sold) || 0
    }
    reportData.value = data
    lines.push(`【${data.periodLabel}经营小结】${data.from} ~ ${data.to}`)
    lines.push(`店铺：${data.shop}`)
    lines.push('')
    lines.push(`销售额　${fmtMoney(sales)}`)
    lines.push(`已售成本 ${fmtMoney(cost)}`)
    lines.push(`净利润　 ${fmtMoney(profit)}（利润率 ${sales > 0 ? margin.toFixed(1) + '%' : '—'}）`)
    lines.push(`　其中：运费 ${fmtMoney(data.shipping)} · 服务费 ${fmtMoney(data.fee)}`)
    lines.push('')
    lines.push(`成交 ${fmtNum(data.soldCount)} 单 · 平均每单利润 ${fmtMoney(data.avgProfit)}`)
    lines.push(`在库成本 ${fmtMoney(data.holdingCost)} · 待售 ${fmtNum(data.pendingCount)} 件`)
    lines.push(`总投入　 ${fmtMoney(data.totalInvest)}`)
    if (data.settledProfit) lines.push(`完结单净利润 ${fmtMoney(data.settledProfit)}（已完结 ${fmtNum(data.settledSold)} 单）`)
    lines.push('')
    lines.push('口径：净利润 = 销售额 − 已售成本（货品成本+运费+服务费），未卖出的进货不计入。')
    report.value = lines.join('\n')
  } catch (e) {
    reportData.value = null
    err.value = e.message || '生成失败'
  } finally { loading.value = false }
}

async function copy() {
  if (!report.value) return
  try {
    await navigator.clipboard.writeText(report.value)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
    return true
  } catch (e) {
    // 剪贴板不可用时退回选中文本
    err.value = '复制失败，请手动选中文本复制'
    return false
  }
}

async function copyAndClose() {
  const ok = await copy()
  if (ok) setTimeout(() => emit('close'), 260)
}

// 分享长图：把文字报告画到 Canvas 上导出 PNG，方便直接发微信
const sharing = ref(false)
async function shareImage() {
  if (!report.value || !reportData.value || sharing.value) return
  sharing.value = true
  try {
    const data = reportData.value
    const W = 960, H = 1240, pad = 64
    const c = document.createElement('canvas')
    c.width = W * 2; c.height = H * 2
    const g = c.getContext('2d')
    g.scale(2, 2)

    const roundRect = (x, y, w, h, r) => {
      g.beginPath()
      g.moveTo(x + r, y)
      g.arcTo(x + w, y, x + w, y + h, r)
      g.arcTo(x + w, y + h, x, y + h, r)
      g.arcTo(x, y + h, x, y, r)
      g.arcTo(x, y, x + w, y, r)
      g.closePath()
    }

    g.fillStyle = '#fffdf7'
    g.fillRect(0, 0, W, H)
    const topGradient = g.createLinearGradient(0, 0, W, 0)
    topGradient.addColorStop(0, '#f4b400')
    topGradient.addColorStop(.55, '#e59a00')
    topGradient.addColorStop(1, '#3f73df')
    g.fillStyle = topGradient
    g.fillRect(0, 0, W, 12)

    g.fillStyle = 'rgba(244, 180, 0, .10)'
    g.beginPath(); g.arc(W - 45, 155, 190, 0, Math.PI * 2); g.fill()
    g.fillStyle = 'rgba(63, 115, 223, .07)'
    g.beginPath(); g.arc(W - 10, 1030, 230, 0, Math.PI * 2); g.fill()

    g.fillStyle = '#211c11'
    g.font = '800 22px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('鱼助手 · 经营报告', pad, 78)
    g.fillStyle = '#8a7e65'
    g.font = '600 18px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText(`${data.shop}  /  ${data.from} — ${data.to}`, pad, 108)

    g.fillStyle = '#1c1912'
    g.font = '900 46px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText(`${data.periodLabel}经营小结`, pad, 190)
    g.fillStyle = '#6e6452'
    g.font = '500 20px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('成交、成本与利润的一页式经营快照', pad, 226)

    const cardY = 270, gap = 18, cardW = (W - pad * 2 - gap * 2) / 3, cardH = 174
    const cards = [
      { label: '净利润', value: fmtMoney(data.profit), note: data.sales > 0 ? `利润率 ${data.margin.toFixed(1)}%` : '利润率 —', accent: data.profit >= 0 ? '#167950' : '#c2412f' },
      { label: '销售额', value: fmtMoney(data.sales), note: `成交 ${fmtNum(data.soldCount)} 单`, accent: '#315fbd' },
      { label: '已售成本', value: fmtMoney(data.cost), note: `平均利润 ${fmtMoney(data.avgProfit)}`, accent: '#9a6200' }
    ]
    cards.forEach((card, i) => {
      const x = pad + i * (cardW + gap)
      g.fillStyle = '#ffffff'
      roundRect(x, cardY, cardW, cardH, 22); g.fill()
      g.strokeStyle = '#eee6d4'; g.lineWidth = 2; g.stroke()
      g.fillStyle = card.accent
      roundRect(x + 22, cardY + 22, 38, 5, 3); g.fill()
      g.fillStyle = '#80755f'
      g.font = '700 18px "PingFang SC", "Microsoft YaHei", sans-serif'
      g.fillText(card.label, x + 22, cardY + 58)
      g.fillStyle = card.accent
      g.font = '900 36px "PingFang SC", "Microsoft YaHei", sans-serif'
      g.fillText(card.value, x + 22, cardY + 108, cardW - 44)
      g.fillStyle = '#8a806b'
      g.font = '500 17px "PingFang SC", "Microsoft YaHei", sans-serif'
      g.fillText(card.note, x + 22, cardY + 142)
    })

    const panelX = pad, panelY = 480, panelW = W - pad * 2, panelH = 384
    g.fillStyle = '#ffffff'
    roundRect(panelX, panelY, panelW, panelH, 24); g.fill()
    g.strokeStyle = '#eee7d7'; g.lineWidth = 2; g.stroke()
    g.fillStyle = '#282217'
    g.font = '800 21px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('经营构成', panelX + 26, panelY + 42)
    g.fillStyle = '#9b9079'
    g.font = '500 16px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('运费、服务费与在库情况', panelX + 26, panelY + 70)
    const details = [
      ['运费支出', fmtMoney(data.shipping), '服务费合计', fmtMoney(data.fee)],
      ['成交单数', `${fmtNum(data.soldCount)} 单`, '平均每单利润', fmtMoney(data.avgProfit)],
      ['在库成本', fmtMoney(data.holdingCost), '待售件数', `${fmtNum(data.pendingCount)} 件`],
      ['总投入', fmtMoney(data.totalInvest), '完结单利润', data.settledProfit ? fmtMoney(data.settledProfit) : '—']
    ]
    details.forEach((row, i) => {
      const y = panelY + 112 + i * 64
      g.strokeStyle = i ? '#f0eadf' : 'transparent'
      g.lineWidth = 1
      if (i) { g.beginPath(); g.moveTo(panelX + 26, y - 21); g.lineTo(panelX + panelW - 26, y - 21); g.stroke() }
      for (let col = 0; col < 2; col++) {
        const x = panelX + 26 + col * (panelW / 2)
        g.fillStyle = '#8f8572'
        g.font = '600 16px "PingFang SC", "Microsoft YaHei", sans-serif'
        g.fillText(row[col * 2], x, y)
        g.fillStyle = '#282217'
        g.font = '800 22px "PingFang SC", "Microsoft YaHei", sans-serif'
        g.fillText(row[col * 2 + 1], x, y + 27)
      }
    })

    g.fillStyle = '#fff8e1'
    roundRect(pad, 890, W - pad * 2, 118, 20); g.fill()
    g.strokeStyle = '#f0d98a'; g.lineWidth = 2; g.stroke()
    g.fillStyle = '#6e5600'
    g.font = '700 18px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('口径说明', pad + 24, 926)
    g.font = '500 17px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText('净利润 = 销售额 − 已售成本（货品成本 + 运费 + 服务费）。', pad + 24, 960)
    g.fillText('未卖出的进货只计入在库成本，不计入已售利润。', pad + 24, 988)

    g.fillStyle = '#a29a8b'
    g.font = '500 16px "PingFang SC", "Microsoft YaHei", sans-serif'
    g.fillText(`由「鱼助手」生成 · ${new Date().toLocaleDateString('zh-CN')}`, pad, 1128)
    g.textAlign = 'right'
    g.fillText('https://github.com/1316195118/Xianyu-Assistant', W - pad, 1128)
    g.textAlign = 'left'

    const a = document.createElement('a')
    a.download = `经营报告-${data.periodLabel}-${data.from}-${data.to}.png`
    a.href = c.toDataURL('image/png')
    a.click()
  } catch (e) {
    err.value = '生成图片失败：' + (e.message || e)
  } finally { sharing.value = false }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="cp-mask" @click.self="emit('close')">
      <div class="report-panel" role="dialog" aria-modal="true" aria-label="周报月报">
        <div class="pc-head">
          <div><b>经营报告</b><span class="muted">周报 / 月报 · 可预览、复制或分享长图</span></div>
          <button class="btn btn-sm btn-quiet" aria-label="关闭" @click="emit('close')">✕</button>
        </div>
        <div class="rp-toolbar">
          <div class="seg seg-sm rp-seg" role="group" aria-label="报告周期">
            <button :class="{ 'on-sell': period === 'week' }" :aria-pressed="period === 'week'" @click="period = 'week'">本周</button>
            <button :class="{ 'on-sell': period === 'month' }" :aria-pressed="period === 'month'" @click="period = 'month'">本月</button>
          </div>
          <div class="rp-actions">
            <button class="btn btn-sm btn-primary" :disabled="loading" @click="generate">
              <span v-if="loading" class="spin"></span>{{ loading ? '生成中' : '生成报告' }}
            </button>
            <button v-if="report" class="btn btn-sm" @click="copy">{{ copied ? '✓ 已复制' : '复制' }}</button>
            <button v-if="report" class="btn btn-sm" :disabled="sharing" @click="shareImage">
              <span v-if="sharing" class="spin"></span>{{ sharing ? '生成中' : '分享长图' }}
            </button>
            <button v-if="report" class="btn btn-sm btn-quiet" :disabled="loading" @click="copyAndClose">复制并关闭</button>
          </div>
        </div>
        <span class="rp-sr" aria-live="polite">{{ loading ? '正在生成经营报告' : (reportData ? '经营报告已生成' : '') }}</span>
        <div v-if="err" class="alert-err" role="alert">{{ err }}</div>
        <div v-if="loading && !reportData" class="rp-loading" aria-hidden="true">
          <div class="skeleton rp-skel-title"></div>
          <div class="rp-skel-grid"><span class="skeleton"></span><span class="skeleton"></span><span class="skeleton"></span></div>
          <div class="skeleton rp-skel-body"></div>
        </div>
        <div v-else-if="reportData" class="rp-preview">
          <article class="rp-sheet">
            <header class="rp-sheet-head">
              <div class="rp-brandline">
                <span class="rp-brandmark" aria-hidden="true">鱼</span>
                <span><b>鱼助手</b><small>经营报告</small></span>
              </div>
              <div class="rp-head-copy">
                <span class="rp-kicker">{{ reportData.periodLabel }} · 经营小结</span>
                <h3>{{ reportData.shop }}</h3>
                <p>{{ reportData.from }} — {{ reportData.to }}</p>
              </div>
              <span class="rp-status" :class="reportData.profit >= 0 ? 'is-pos' : 'is-neg'">
                {{ reportData.profit >= 0 ? '盈利' : '亏损' }}
              </span>
            </header>
            <section class="rp-kpis" aria-label="报告核心指标">
              <div class="rp-kpi is-primary" :class="reportData.profit >= 0 ? 'is-pos' : 'is-neg'">
                <span>净利润</span>
                <b>{{ fmtMoney(reportData.profit) }}</b>
                <small>{{ reportData.sales > 0 ? `利润率 ${reportData.margin.toFixed(1)}%` : '利润率 —' }}</small>
              </div>
              <div class="rp-kpi">
                <span>销售额</span>
                <b>{{ fmtMoney(reportData.sales) }}</b>
                <small>成交 {{ fmtNum(reportData.soldCount) }} 单</small>
              </div>
              <div class="rp-kpi">
                <span>已售成本</span>
                <b>{{ fmtMoney(reportData.cost) }}</b>
                <small>含运费与服务费</small>
              </div>
            </section>
            <section class="rp-breakdown">
              <div class="rp-section-title"><b>经营构成</b><span>费用、成交与库存</span></div>
              <div class="rp-detail-grid">
                <div><span>运费支出</span><b>{{ fmtMoney(reportData.shipping) }}</b></div>
                <div><span>服务费合计</span><b>{{ fmtMoney(reportData.fee) }}</b></div>
                <div><span>平均每单利润</span><b>{{ fmtMoney(reportData.avgProfit) }}</b></div>
                <div><span>在库成本</span><b>{{ fmtMoney(reportData.holdingCost) }}</b></div>
                <div><span>待售件数</span><b>{{ fmtNum(reportData.pendingCount) }} 件</b></div>
                <div><span>总投入</span><b>{{ fmtMoney(reportData.totalInvest) }}</b></div>
                <div class="rp-detail-wide"><span>完结单净利润</span><b>{{ reportData.settledProfit ? fmtMoney(reportData.settledProfit) : '—' }}</b><small v-if="reportData.settledProfit">已完结 {{ fmtNum(reportData.settledSold) }} 单</small></div>
              </div>
            </section>
            <div class="rp-method">
              <b>口径说明</b>
              <span>净利润 = 销售额 − 已售成本（货品成本 + 运费 + 服务费）。未卖出的进货只计入在库成本，不计入已售利润。</span>
            </div>
            <details class="rp-raw">
              <summary>查看可复制的文字版</summary>
              <textarea class="rp-text" :value="report" readonly spellcheck="false" wrap="soft" aria-label="经营报告文字版"></textarea>
            </details>
            <footer class="rp-sheet-foot">鱼助手 · 当前店铺数据快照</footer>
          </article>
        </div>
        <div v-else class="rp-empty muted">打开面板会自动生成当前周期的报告，也可以选择周期后重新生成。</div>
      </div>
    </div>
  </Teleport>
</template>
