<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { api } from '../api'
import { localRecognize } from '../ocr'
import { shops, activeId } from '../shopStore'
import { maskCloser } from '../maskClose'
// 遮罩点击守卫：在输入框里拖选文字划出弹窗松手时，不会误关弹窗
const mask = maskCloser(() => close)


// 批量导入：一次选多张订单截图 → 顺序识别（复用同一个 OCR worker）→ 核对表格 → 逐条入库。
// 为什么要顺序：tesseract 的 worker 是单实例、WASM 内存有限，并发识别长图必崩；
// 而且顺序跑能给出「第 3/12 张」这种确定进度，比一堆并行的百分比好读。
const emit = defineEmits(['close', 'done'])

const MAX_FILES = 30
const step = ref('pick')        // pick | run | review | done
const rows = ref([])
const running = ref(false)
const importing = ref(false)
const aborted = ref(false)
const importedOk = ref(0)
const importedFail = ref(0)
const hint = ref('')
// 网页版脚本导出的 JSON：和截图走同一张核对表格，只是没有图片
const jsonOpen = ref(false)
const jsonText = ref('')
const jsonErr = ref('')
const skipNote = ref('')   // 本次解析跳过了哪些，带到核对页也看得见
const shopName = computed(() => (shops.value.find(s => s.id === activeId.value) || {}).name || '当前店铺')

let seq = 0
const num = (v) => Number(v) || 0
const hasShip = (v) => num(v) > 0.1
const round2 = (v) => Math.round(v * 100) / 100

function addFiles(fileList) {
  const files = Array.from(fileList || []).filter((f) => /^image\//.test(f.type))
  hint.value = ''
  if (!files.length) { hint.value = '只支持图片文件'; return }
  const room = MAX_FILES - rows.value.length
  if (room <= 0) { hint.value = `一次最多 ${MAX_FILES} 张，请先导入一批再继续`; return }
  if (files.length > room) hint.value = `一次最多 ${MAX_FILES} 张，已加入前 ${room} 张`
  for (const f of files.slice(0, room)) {
    rows.value.push({
      key: ++seq, file: f, thumb: URL.createObjectURL(f),
      state: 'pending', progress: 0, error: '',
      side: 'buy', product: '', price: 0, net: 0, fee: 0, wuyou: 0, shipping: 0,
      order_no: '', time: '', counterparty: '', approx: false, garbled: false,
      picked: true, dupId: 0, result: '', resultMsg: ''
    })
  }
}

function onPick(e) {
  addFiles(e.target.files)
  e.target.value = ''   // 允许重复选同一张
}
function onDrop(e) { addFiles(e.dataTransfer?.files) }
function removeRow(r) {
  try { URL.revokeObjectURL(r.thumb) } catch (e) { /* 已释放 */ }
  rows.value = rows.value.filter((x) => x.key !== r.key)
}

function applyOcr(r, d) {
  r.side = d.side === 'sell' ? 'sell' : 'buy'
  r.product = String(d.product || '').trim()
  r.price = num(d.price)
  r.net = num(d.net_price)
  r.fee = num(d.fee)
  r.wuyou = num(d.wuyou_fee)
  r.shipping = num(d.shipping)
  r.order_no = String(d.order_no || '').trim()
  r.time = String(d.deal_time || '').trim()
  r.counterparty = String(d.counterparty || '').trim()
  r.approx = !!d.deal_time_approx
  r.garbled = !!d.amount_garbled
}

const imgRows = computed(() => rows.value.filter((r) => r.file))
// 全是图片时说「张」，混了网页数据就说「条」
const unit = computed(() => (rows.value.length && imgRows.value.length === rows.value.length) ? '张' : '条')
async function start() {
  if (!imgRows.value.length || running.value) return
  step.value = 'run'
  running.value = true
  aborted.value = false
  for (const r of rows.value) {
    if (aborted.value) break
    if (!r.file || r.state === 'done') continue
    r.state = 'running'; r.progress = 0; r.error = ''
    try {
      const res = await localRecognize(r.file, (p) => { if (p >= 0) r.progress = p })
      applyOcr(r, res.fields || {})
      r.state = 'done'
    } catch (e) {
      r.state = 'error'
      r.error = e.message || '识别失败'
    }
  }
  running.value = false
  await goReview()
}
function stopRun() { aborted.value = true }

async function goReview() {
  const done = rows.value.filter((r) => r.state === 'done')
  if (!done.length) { step.value = 'pick'; hint.value = '这一批都没识别成功，可以换「视觉大模型」引擎再试'; return }
  const nos = done.map((r) => r.order_no).filter(Boolean)
  if (nos.length) {
    try {
      const res = await api.existsOrders(nos)
      const map = res.existing || {}
      for (const r of done) r.dupId = r.order_no ? (map[r.order_no] || 0) : 0
    } catch (e) { /* 查重失败不阻断导入，只是不标重复 */ }
  }
  // 勾选代表「我想导入」，能不能导由 statusOf 决定：
  // 缺字段的行补全后会自动重新计入，不用再手动勾一次。
  for (const r of rows.value) r.picked = true
  step.value = 'review'
}

// 行状态：决定能不能勾选导入。重复与识别失败一律不可导入，避免把脏数据灌进去。
function statusOf(r) {
  if (r.state === 'error') return { k: 'err', t: '识别失败' }
  if (r.state !== 'done') return { k: 'idle', t: '未识别' }
  if (r.dupId) return { k: 'dup', t: '库里已有' }
  if (!r.product) return { k: 'warn', t: '缺商品名' }
  const price = num(r.price), net = num(r.net)
  if (r.side === 'buy' && price <= 0) return { k: 'warn', t: '缺金额' }
  if (r.side === 'sell' && price <= 0 && net <= 0) return { k: 'warn', t: '缺金额' }
  return { k: 'ok', t: '可导入' }
}
const pickable = (r) => statusOf(r).k === 'ok'
const pickedRows = computed(() => rows.value.filter((r) => r.picked && pickable(r)))
const okCount = computed(() => rows.value.filter((r) => statusOf(r).k === 'ok').length)
const dupCount = computed(() => rows.value.filter((r) => statusOf(r).k === 'dup').length)
const badCount = computed(() => rows.value.filter((r) => ['warn', 'err'].includes(statusOf(r).k)).length)

function toggleAll(on) {
  for (const r of rows.value) r.picked = on ? pickable(r) : false
}

// 卖单到手价：识别不到就用「成交价 − 运费 − 服务费 − 无忧卖」推，和明细页口径一致
const netOf = (r) => {
  const n = num(r.net)
  if (n > 0) return n
  return Math.max(0, round2(num(r.price) - num(r.shipping) - num(r.fee) - num(r.wuyou)))
}

function payloadOf(r) {
  const price = num(r.price), ship = num(r.shipping)
  const base = {
    product: r.product.trim(), quantity: 1, unit_cost: 0, parent_id: 0, note: '',
    buy_price: 0, buy_shipping: 0, buy_ship_tag: '', buy_order_no: '', buy_time: '', buy_counterparty: '',
    sell_price: 0, sell_fee: 0, sell_wuyou_fee: 0, sell_net: 0, sell_shipping: 0, sell_ship_tag: '',
    sell_order_no: '', sell_time: '', sell_counterparty: '', side: r.side
  }
  if (r.side === 'buy') {
    return {
      ...base,
      buy_price: price, buy_shipping: ship,
      buy_ship_tag: hasShip(ship) ? '到付' : '包邮',
      buy_order_no: r.order_no, buy_time: r.time, buy_counterparty: r.counterparty
    }
  }
  return {
    ...base,
    sell_price: price, sell_fee: num(r.fee), sell_wuyou_fee: num(r.wuyou),
    sell_net: netOf(r), sell_shipping: ship,
    sell_ship_tag: hasShip(ship) ? '卖家承担' : '买家承担',
    sell_order_no: r.order_no, sell_time: r.time, sell_counterparty: r.counterparty
  }
}

async function doImport() {
  const list = pickedRows.value
  if (!list.length || importing.value) return
  importing.value = true
  importedOk.value = 0
  importedFail.value = 0
  for (const r of list) {
    try {
      await api.create(payloadOf(r))
      r.result = 'ok'
      r.dupId = -1   // 本批次内已导入，避免重复点
      importedOk.value++
    } catch (e) {
      r.result = 'err'
      r.resultMsg = e.message || '保存失败'
      importedFail.value++
    }
  }
  importing.value = false
  step.value = 'done'
  emit('done', importedOk.value)
}

// 网页版脚本产出的 JSON：{ source, side, rows:[{order_no,product,price,net,fee,shipping,time,counterparty}] }
// 也接受裸数组，方便手工整理的数据直接灌进来。
function parseJson() {
  jsonErr.value = ''
  const text = jsonText.value.trim()
  if (!text) { jsonErr.value = '请先把导出的内容粘贴进来'; return }
  let data
  try { data = JSON.parse(text) } catch (e) { jsonErr.value = '不是合法的 JSON：' + (e.message || ''); return }
  const list = Array.isArray(data) ? data : (data.rows || data.orders || data.list || [])
  if (!Array.isArray(list) || !list.length) { jsonErr.value = '没找到订单数组（期望 rows / orders / 裸数组）'; return }
  const defSide = data.side === 'sell' ? 'sell' : 'buy'
  const known = new Set(rows.value.map((r) => r.order_no).filter(Boolean))
  // 跳过原因分开计数，用户才知道到底为什么少了
  const skip = { refund: 0, tiny: 0, dup: 0, bad: 0 }
  let added = 0
  for (const it of list) {
    if (!it || typeof it !== 'object') { skip.bad++; continue }
    const no = String(it.order_no || it.orderNo || '').trim()
    const product = String(it.product || it.title || '').trim()
    const price = num(it.price)

    // 只收「交易成功」：退款、关闭、未完成一律不进账。
    // status / status_enum 由导出脚本带回；手工 JSON 没有这两个字段时不做这层过滤。
    const st = String(it.status || '').trim()
    const se = String(it.status_enum || '').trim()
    if (st || se) {
      const refunded = /退款/.test(st) || se === 'refund_success'
      const closed = /关闭/.test(st) || se === 'close_by_buyer'
      const done = st ? st === '交易成功' : se === 'trade_success'
      if (refunded || closed || !done) { skip.refund++; continue }
    }
    // ¥0.01 这类占位单（闲鱼上常见的「勿拍」）不进账，会把均价拉歪
    if (price <= 0.05) { skip.tiny++; continue }

    if (!product && price <= 0) { skip.bad++; continue }
    if (no && known.has(no)) { skip.dup++; continue }
    if (no) known.add(no)
    rows.value.push({
      key: ++seq, file: null, thumb: '',
      state: 'done', progress: 0, error: '',
      side: it.side === 'sell' ? 'sell' : (it.side === 'buy' ? 'buy' : defSide),
      product, price,
      net: num(it.net), fee: num(it.fee), wuyou: num(it.wuyou), shipping: num(it.shipping),
      order_no: no, time: String(it.time || '').trim(), counterparty: String(it.counterparty || '').trim(),
      approx: false, garbled: false, picked: true, dupId: 0, result: '', resultMsg: ''
    })
    added++
  }
  const why = []
  if (skip.refund) why.push(`退款/未完成 ${skip.refund}`)
  if (skip.tiny) why.push(`金额过小 ${skip.tiny}`)
  if (skip.dup) why.push(`重复 ${skip.dup}`)
  if (skip.bad) why.push(`缺字段 ${skip.bad}`)
  if (!added) { jsonErr.value = `没有可导入的新订单（${why.join('、') || '内容为空'}）`; return }
  jsonText.value = ''
  jsonOpen.value = false
  hint.value = why.length ? `已加入 ${added} 条，跳过 ${why.join('、')}` : ''
  skipNote.value = why.length ? `已跳过：${why.join('、')}` : ''
  goReview()
}

function close() {
  if (importing.value || running.value) return
  rows.value.forEach((r) => { try { URL.revokeObjectURL(r.thumb) } catch (e) { /* ignore */ } })
  emit('close')
}
const onEscKey = (e) => { if (e.key === 'Escape') close() }
onMounted(() => document.addEventListener('keydown', onEscKey))

onUnmounted(() => {
  document.removeEventListener('keydown', onEscKey)
  rows.value.forEach((r) => { try { URL.revokeObjectURL(r.thumb) } catch (e) { /* ignore */ } })
})
</script>

<template>
  <div class="modal-mask" @pointerdown="mask.onPointerDown" @click="mask.onClick">
    <div class="modal modal-wide bi-panel">
      <div class="modal-head">
        <h3>
          批量导入截图
          <span class="bi-head-sub">一次识别多张，核对后统一入库</span>
        </h3>
        <button class="x" :disabled="running || importing" @click="close" aria-label="关闭">×</button>
      </div>

      <div class="modal-body bi-body">
        <!-- ① 选图 -->
        <template v-if="step === 'pick'">
          <label class="bi-drop" @dragover.prevent @drop.prevent="onDrop">
            <input type="file" accept="image/*" multiple class="bi-file" @change="onPick" />
            <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 16V4"/><path d="M7 9l5-5 5 5"/><path d="M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/></svg>
            <b>点击选择 或 拖拽多张订单截图</b>
            <span>一次最多 30 张。买进、卖出可以混在一起，识别后按图片自动判断类型</span>
          </label>
          <p class="bi-target muted">将导入到「<b>{{ shopName }}</b>」<span v-if="shops.length > 1">，要换店请先在顶栏切换店铺</span></p>
          <p v-if="hint" class="bi-hint">{{ hint }}</p>

          <div v-if="rows.length" class="bi-thumbs">
            <div v-for="r in rows" :key="r.key" class="bi-thumb-cell">
              <img :src="r.thumb" :alt="`待识别订单截图：${r.file?.name || '未命名图片'}`"
                   width="76" height="101" loading="lazy" decoding="async" />
              <button class="bi-thumb-x" @click="removeRow(r)" aria-label="移除">×</button>
            </div>
          </div>
          <p v-if="rows.length" class="bi-count muted">
            已加入 <b>{{ rows.length }}</b> 条<span v-if="imgRows.length < rows.length">（其中 {{ imgRows.length }} 张图待识别）</span>
          </p>

          <!-- 网页版脚本导出的数据走同一个核对表格，只是没有图片 -->
          <div class="bi-json">
            <button v-if="!jsonOpen" class="link bi-json-open" @click="jsonOpen = true">
              从闲鱼网页版粘贴 JSON（不用截图，一次可导入整页订单）
            </button>
            <div v-else class="bi-json-box">
              <textarea v-model="jsonText" class="bi-json-ta" spellcheck="false"
                        placeholder='把脚本导出的 JSON 粘贴到这里，形如 {"source":"goofish","rows":[{"product":"...","price":180}]}'></textarea>
              <div class="bi-json-actions">
                <span class="muted">
                  还没装脚本？<a class="link" href="/goofish-export.user.js" target="_blank" rel="noopener">点这里安装</a>
                  （在 goofish.com 订单页翻页后点「导出 JSON」）
                </span>
                <button class="btn btn-sm btn-quiet" @click="jsonOpen = false; jsonErr = ''">收起</button>
                <button class="btn btn-sm btn-primary" @click="parseJson">解析并核对</button>
              </div>
              <p v-if="jsonErr" class="bi-hint">{{ jsonErr }}</p>
            </div>
          </div>
        </template>

        <!-- ② 识别中 -->
        <template v-else-if="step === 'run'">
          <div class="bi-runbar">
            <span class="spin"></span>
            <span>正在识别第 {{ Math.min(rows.findIndex(r => r.state === 'running' || r.state === 'pending') + 1, rows.length) }} / {{ rows.length }} 张</span>
            <span class="muted">本地 OCR，图片不上传</span>
          </div>
          <div class="bi-queue">
            <div v-for="(r, i) in rows" :key="r.key" class="bi-qrow">
              <img :src="r.thumb" :alt="`正在识别的订单截图 ${i + 1}`"
                   class="bi-qthumb" width="30" height="38" decoding="async" />
              <span class="bi-qname">第 {{ i + 1 }} 张</span>
              <span class="bi-qstate" :class="r.state">
                <template v-if="r.state === 'pending'">等待</template>
                <template v-else-if="r.state === 'running'">识别中 {{ r.progress }}%</template>
                <template v-else-if="r.state === 'done'">已识别</template>
                <template v-else-if="r.state === 'error'">失败</template>
              </span>
            </div>
          </div>
        </template>

        <!-- ③ 核对 -->
        <template v-else-if="step === 'review'">
          <p v-if="skipNote" class="bi-skip">{{ skipNote }}</p>
          <div class="bi-summary">
            <span>共 <b>{{ rows.length }}</b> {{ unit }}</span>
            <span class="ok">可导入 <b>{{ okCount }}</b></span>
            <span v-if="dupCount" class="dup">库里已有 <b>{{ dupCount }}</b></span>
            <span v-if="badCount" class="warn">待补全 <b>{{ badCount }}</b></span>
            <span class="bi-sum-links">
              <button class="link" @click="toggleAll(true)">全选可导入</button>
              <button class="link" @click="toggleAll(false)">全不选</button>
            </span>
          </div>

          <div class="bi-rows">
            <div v-for="(r, i) in rows" :key="r.key" class="bi-row" :class="{ 'is-off': !r.picked || !pickable(r) }">
              <label class="bi-check">
                <input type="checkbox" :checked="r.picked && pickable(r)" :disabled="!pickable(r)" @change="r.picked = $event.target.checked" />
              </label>
              <img v-if="r.thumb" :src="r.thumb" :alt="`第 ${i + 1} 条订单截图：${r.file?.name || r.product || '待核对记录'}`"
                   class="bi-thumb" width="52" height="66" loading="lazy" decoding="async" />
              <div v-else class="bi-thumb bi-thumb-json" title="来自闲鱼网页版导出的数据">JSON</div>
              <div class="bi-main">
                <div class="bi-line1">
                  <span class="bi-idx">{{ i + 1 }}</span>
                  <input class="input bi-product" v-model="r.product" placeholder="商品名称（必填，用于同名配对）" />
                  <span class="bi-badge" :class="statusOf(r).k">{{ statusOf(r).t }}</span>
                </div>
                <div class="bi-line2">
                  <div class="seg bi-seg">
                    <button :class="{ 'on-buy': r.side === 'buy' }" @click="r.side = 'buy'">买进</button>
                    <button :class="{ 'on-sell': r.side === 'sell' }" @click="r.side = 'sell'">卖出</button>
                  </div>
                  <label class="bi-f"><span>成交价</span>
                    <input class="input" type="number" step="0.01" v-model.number="r.price" />
                  </label>
                  <label class="bi-f"><span>运费</span>
                    <input class="input" type="number" step="0.01" v-model.number="r.shipping" />
                  </label>
                  <label v-if="r.side === 'sell'" class="bi-f"><span>服务费</span>
                    <input class="input" type="number" step="0.01" v-model.number="r.fee" />
                  </label>
                  <span v-if="r.side === 'sell'" class="bi-net">到手 ¥{{ netOf(r).toFixed(2) }}</span>
                </div>
                <div class="bi-line3">
                  <span :class="{ muted: !r.time }">{{ r.time || '无时间' }}<i v-if="r.approx" class="bi-approx" title="时间由订单号推断，不是精确值">≈</i></span>
                  <span :class="{ muted: !r.order_no }">{{ r.order_no || '无订单号' }}</span>
                  <span :class="{ muted: !r.counterparty }">{{ r.counterparty || '无昵称' }}</span>
                  <span v-if="r.dupId > 0" class="bi-dupid">已存在 #{{ r.dupId }}</span>
                  <span v-if="r.garbled" class="bi-garbled">金额可能识别不全，请核对</span>
                  <span v-if="r.result === 'err'" class="bi-err">{{ r.resultMsg }}</span>
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- ④ 完成 -->
        <template v-else>
          <div class="bi-done">
            <div class="bi-done-ico">
              <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12l4.5 4.5L19 7"/></svg>
            </div>
            <div class="bi-done-title">已导入 {{ importedOk }} 条</div>
            <div class="bi-done-sub" v-if="importedFail">另有 {{ importedFail }} 条保存失败，原因见上方红字</div>
            <div class="bi-done-sub" v-else>卖单若与已有买进单同名，会自动关联并生成联单</div>
          </div>
        </template>
      </div>

      <div class="modal-foot bi-foot">
        <template v-if="step === 'pick'">
          <span class="foot-hint muted">识别在本机完成，图片不会上传</span>
          <button class="btn btn-quiet" @click="close">取消</button>
          <button class="btn btn-primary" :disabled="!imgRows.length" @click="start">开始识别{{ imgRows.length ? ` ${imgRows.length} 张` : '' }}</button>
        </template>
        <template v-else-if="step === 'run'">
          <span class="foot-hint muted">已识别 <b>{{ rows.filter(r => r.state === 'done').length }}</b> / {{ rows.length }}</span>
          <button class="btn btn-quiet" :disabled="aborted" @click="stopRun">{{ aborted ? '正在停止…' : '中止' }}</button>
        </template>
        <template v-else-if="step === 'review'">
          <span class="foot-hint muted">勾选的行会逐条写入当前店铺</span>
          <button class="btn btn-quiet" :disabled="importing" @click="step = 'pick'; skipNote = ''">返回选图</button>
          <button class="btn btn-primary" :disabled="!pickedRows.length || importing" @click="doImport">
            <span v-if="importing" class="spin"></span>{{ importing ? '导入中…' : `导入 ${pickedRows.length} 条` }}
          </button>
        </template>
        <template v-else>
          <span class="foot-hint muted">可在明细里继续编辑或删除</span>
          <button class="btn btn-primary" @click="close">完成</button>
        </template>
      </div>
    </div>
  </div>
</template>
