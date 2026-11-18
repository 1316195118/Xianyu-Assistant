<script setup>
/**
 * 定价速算：卖货时快速算「卖多少钱不亏 / 能赚多少」。
 * 纯前端计算，不碰后端数据；从命令面板或首页入口打开。
 *
 * 两种模式：
 *  - price（正推）：已知成本，按目标利润率算建议售价
 *  - profit（倒推）：已知售价，算到手价与净利润
 *
 * 口径与系统统计一致：
 *  利润 = 成交价 − 货品成本(单件成本×件数) − 买运费分摊 − 卖运费 − 软件服务费 − 无忧卖服务费
 *  到手价 = 成交价 − 卖运费 − 软件服务费 − 无忧卖服务费
 * 服务费按「成交价 × 费率」计算，与真实平台计费方式一致。
 */
import { ref, computed, watch, nextTick } from 'vue'
import { fmtMoney } from '../utils'

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['close', 'use'])

const mode = ref('price')          // price=按成本定价 / profit=按售价核算
const unitCost = ref(0)            // 单件货品成本
const buyShip = ref(0)             // 买运费分摊（每件）
const sellShip = ref(0)            // 卖运费
const feeRate = ref(1.5)           // 软件服务费率 %
const wuyouRate = ref(0)           // 无忧卖服务费率 %
const targetRate = ref(30)         // 目标利润率 %
const sellPrice = ref(0)           // 已知售价（倒推模式）
const qty = ref(1)
const inputEl = ref(null)

const n = (v) => Math.max(0, Number(v) || 0)

// 每件货品的总成本 = 单件成本 + 买运费分摊
const perGoods = computed(() => n(unitCost.value) + n(buyShip.value))
// 服务费合计（按当前售价/建议售价的费率）
const feeOf = (price) => (n(price) * (n(feeRate.value) + n(wuyouRate.value))) / 100

// 正推：售价 P，利润率 = (P − 货品×件 − 卖运费 − 服务费×P) / P
// 即 P×(1 − 费率) − 货品×件 − 卖运费 = P×目标率
//   P×(1 − 费率 − 目标率) = 货品×件 + 卖运费
const suggestPrice = computed(() => {
  const denom = 1 - (n(feeRate.value) + n(wuyouRate.value)) / 100 - n(targetRate.value) / 100
  if (denom <= 0) return 0    // 费率+目标率 >= 100%，无解
  const goodsAll = perGoods.value * Math.max(1, n(qty.value) || 1)
  return Math.round(((goodsAll + n(sellShip.value)) / denom) * 100) / 100
})

// 倒推结果：基于用户填的售价
const calc = computed(() => {
  const price = n(sellPrice.value)
  if (price <= 0) return null
  const goods = perGoods.value * Math.max(1, n(qty.value) || 1)
  const fee = feeOf(price)
  const ship = n(sellShip.value)
  const net = Math.round((price - ship - fee) * 100) / 100      // 到手价
  const profit = Math.round((net - goods) * 100) / 100          // 净利润
  const rate = price > 0 ? (profit / price) * 100 : 0
  return { goods, fee, ship, net, profit, rate }
})

// 正推结果：按建议售价展示
const priceCalc = computed(() => {
  const price = suggestPrice.value
  if (price <= 0) return null
  const goods = perGoods.value * Math.max(1, n(qty.value) || 1)
  const fee = feeOf(price)
  const ship = n(sellShip.value)
  const net = Math.round((price - ship - fee) * 100) / 100
  const profit = Math.round((net - goods) * 100) / 100
  return { price, goods, fee, ship, net, profit, rate: n(targetRate.value) }
})

watch(() => props.open, async (v) => {
  if (!v) return
  await nextTick()
  inputEl.value?.focus()
})

function onKey(e) {
  if (e.key === 'Escape') { e.stopPropagation(); emit('close') }
}
// 把当前结果带进录入弹窗：卖出侧的成交价、单件成本、卖运费、服务费都预填好。
function useInRecord() {
  const r = mode.value === 'price' ? priceCalc.value : calc.value
  if (!r) return
  emit('use', {
    sell_price: mode.value === 'price' ? r.price : n(sellPrice.value),
    unit_cost: n(unitCost.value),
    sell_shipping: n(sellShip.value),
    sell_fee: Math.round((r.price || n(sellPrice.value)) * n(feeRate.value)) / 100,
    sell_wuyou_fee: Math.round((r.price || n(sellPrice.value)) * n(wuyouRate.value)) / 100
  })
  emit('close')
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="cp-mask" @click.self="emit('close')" @keydown="onKey">
      <div class="price-calc" role="dialog" aria-modal="true" aria-label="定价速算">
        <div class="pc-head">
          <div>
            <b>定价速算</b>
            <span class="muted">按成本推售价，或按售价算利润</span>
          </div>
          <button class="btn btn-sm btn-quiet" aria-label="关闭" @click="emit('close')">✕</button>
        </div>

        <div class="pc-tabs">
          <button :class="{ on: mode === 'price' }" @click="mode = 'price'">按成本定价</button>
          <button :class="{ on: mode === 'profit' }" @click="mode = 'profit'">按售价核算</button>
        </div>

        <div class="pc-body">
          <div class="pc-fields">
            <label class="pc-field">
              <span>{{ mode === 'price' ? '单件成本' : '单件成本' }}<span class="pc-q" title="这批货每件的进货价，含买运费分摊填在下面也行">?</span></span>
              <input ref="inputEl" class="input num" type="number" min="0" step="0.01" v-model.number="unitCost" placeholder="0.00" />
            </label>
            <label class="pc-field">
              <span>买运费 / 件<span class="pc-q" title="每件摊到的买运费，没有就填 0">?</span></span>
              <input class="input num" type="number" min="0" step="0.01" v-model.number="buyShip" placeholder="0.00" />
            </label>
            <label class="pc-field">
              <span>卖运费<span class="pc-q" title="寄给买家承担的快递费">?</span></span>
              <input class="input num" type="number" min="0" step="0.01" v-model.number="sellShip" placeholder="0.00" />
            </label>
            <label class="pc-field">
              <span>软件服务费 %<span class="pc-q" title="闲鱼按成交价收取的比例，常见 1.5%">?</span></span>
              <input class="input num" type="number" min="0" max="100" step="0.1" v-model.number="feeRate" />
            </label>
            <label class="pc-field">
              <span>无忧卖 %<span class="pc-q" title="无忧卖类目服务费比例，没开就填 0">?</span></span>
              <input class="input num" type="number" min="0" max="100" step="0.1" v-model.number="wuyouRate" />
            </label>
            <label class="pc-field">
              <span>件数</span>
              <input class="input num" type="number" min="1" step="1" v-model.number="qty" />
            </label>

            <template v-if="mode === 'price'">
              <label class="pc-field">
                <span>目标利润率 %<span class="pc-q" title="利润占成交价的比例，改这个数下面的建议售价会跟着变">?</span></span>
                <input class="input num" type="number" min="0" max="99" step="1" v-model.number="targetRate" />
              </label>
            </template>
            <template v-else>
              <label class="pc-field">
                <span>计划售价<span class="pc-q" title="你打算挂多少钱">?</span></span>
                <input class="input num" type="number" min="0" step="0.01" v-model.number="sellPrice" placeholder="0.00" />
              </label>
            </template>
          </div>

          <div class="pc-result" :class="{ 'is-neg': (mode === 'price' ? priceCalc?.profit : calc?.profit) < 0 }">
            <template v-if="mode === 'price'">
              <div v-if="!priceCalc" class="pc-no">目标利润率或费率填得太高，算不出可行售价。把目标利润率调低试试。</div>
              <template v-else>
                <div class="pc-big"><span>建议售价</span><b class="num">{{ fmtMoney(priceCalc.price) }}</b></div>
                <div class="pc-line"><span>预计到手</span><b class="num">{{ fmtMoney(priceCalc.net) }}</b></div>
                <div class="pc-line"><span>预计利润</span><b class="num" :class="priceCalc.profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(priceCalc.profit) }}</b></div>
                <div class="pc-line"><span>利润率</span><b class="num">{{ priceCalc.rate.toFixed(1) }}%</b></div>
                <div class="pc-break">货品 {{ fmtMoney(priceCalc.goods) }} + 卖运费 {{ fmtMoney(priceCalc.ship) }} + 服务费 {{ fmtMoney(priceCalc.fee) }}</div>
              </template>
            </template>
            <template v-else>
              <div v-if="!calc" class="pc-no">填上计划售价，这里会实时算出到手价和利润。</div>
              <template v-else>
                <div class="pc-big"><span>预计利润</span><b class="num" :class="calc.profit >= 0 ? 'pos' : 'neg'">{{ fmtMoney(calc.profit) }}</b></div>
                <div class="pc-line"><span>利润率</span><b class="num">{{ calc.rate.toFixed(1) }}%</b></div>
                <div class="pc-line"><span>到手价</span><b class="num">{{ fmtMoney(calc.net) }}</b></div>
                <div class="pc-line"><span>服务费</span><b class="num">{{ fmtMoney(calc.fee) }}</b></div>
                <div class="pc-break">货品 {{ fmtMoney(calc.goods) }} + 卖运费 {{ fmtMoney(calc.ship) }}，服务费按售价自动扣</div>
              </template>
            </template>
          </div>
        </div>

        <div class="pc-foot" :class="{ muted: !((mode === 'price' ? priceCalc : calc)) }">
          <button v-if="mode === 'price' ? priceCalc : calc" class="btn btn-sm btn-primary" @click="useInRecord">
            用这个价录入
          </button>
          <span v-if="mode === 'price' ? priceCalc : calc"> · 会把售价、成本和费用填进新记录</span>
          <span v-else>结果仅供参考，实际以成交和平台账单为准</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

