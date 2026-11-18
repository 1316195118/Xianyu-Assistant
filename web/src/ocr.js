// 本地离线 OCR：tesseract.js + WASM + 中文语言包，全部资源随程序内嵌，不联网
import { api } from './api'
import { salvageFields } from './salvage.js'

export { salvageFields } from './salvage.js'

let workerPromise = null

async function getWorker(onProgress) {
  if (!workerPromise) {
    workerPromise = (async () => {
      const { createWorker } = await import('tesseract.js')
      const w = await createWorker('chi_sim', 1, {
        workerPath: '/ocr/worker.min.js',
        corePath: '/ocr/tesseract-core-simd-lstm.wasm.js',
        langPath: '/ocr',
        gzip: true,
        logger: (m) => {
          if (m.status === 'recognizing text' && onProgress) {
            onProgress(Math.round((m.progress || 0) * 100))
          }
        }
      })
      return w
    })().catch((e) => {
      workerPromise = null
      throw e
    })
  }
  return workerPromise
}

// ---------- 长截图分段 OCR ----------
// 手机端（尤其微信 WebView / iOS）对 1280×2774 这类超长图，WASM 内存与图片解码
// 限制会导致只识别出顶部约 1/3，下半部分的「订单编号 / 时间 / 昵称」整片丢失。
// 把长图按高度切成若干段分别识别再拼接，每段都是小图，就能完整覆盖。

function loadImage(file) {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => { URL.revokeObjectURL(url); resolve(img) }
    img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('图片加载失败')) }
    img.src = url
  })
}

// ⚠️ 这里刻意「不做」灰度/对比度增强。
// 实测对照：闲鱼订单页右侧的日期是浅灰小字，做线性对比度拉伸会把它们提亮到
// 接近白底，导致 4 个时间全部识别不出来（原图不增强反而 4 个全中）。
// 分段本身已解决「长图下半部分丢失」，任何额外像素处理都可能帮倒忙。

function toBlob(canvas) {
  return new Promise((resolve) => canvas.toBlob(resolve, 'image/png'))
}

// 时间值定向识别：闲鱼订单页「下单时间/付款时间/…」的日期时间是右对齐的浅灰小字，
// 手机端 WASM 常只认出左侧黑色标签、把右侧数值整片漏掉。
// 实测把该区域单独裁出并 2x 放大（不做任何增强，增强会把浅灰字洗白）后，4 个时间全能识别。
// 仅在其它路径都没拿到时间时启用。
async function recognizeTimeRegion(worker, file, onProgress) {
  const img = await loadImage(file)
  const w = img.naturalWidth
  const h = img.naturalHeight
  if (!w || !h) return ''
  const y0 = Math.floor(h * 0.60)
  const y1 = Math.min(h, Math.floor(h * 0.99))
  const regionH = y1 - y0
  const c = document.createElement('canvas')
  c.width = w * 2
  c.height = regionH * 2
  const ctx = c.getContext('2d')
  ctx.fillStyle = '#fff'
  ctx.fillRect(0, 0, c.width, c.height)
  ctx.drawImage(img, 0, y0, w, regionH, 0, 0, w * 2, regionH * 2)
  const blob = await toBlob(c)
  if (!blob) return ''
  onProgress?.(-1, '正在补识别交易时间…')
  const { data } = await worker.recognize(blob)
  return (data?.text || '').trim()
}

async function recognizeSliced(worker, file, onProgress) {
  const img = await loadImage(file)
  const w = img.naturalWidth
  const h = img.naturalHeight
  const MAX_SEG_H = 1000
  if (!w || !h || h <= MAX_SEG_H * 1.3) return ''   // 短图无需分段
  const n = Math.min(4, Math.ceil(h / MAX_SEG_H))
  const segH = Math.ceil(h / n)
  const overlap = 70         // 段间重叠，避免正好切断一行文字
  const texts = []
  for (let i = 0; i < n; i++) {
    const y0 = Math.max(0, i * segH - overlap)
    const y1 = Math.min(h, (i + 1) * segH + overlap)
    const c = document.createElement('canvas')
    c.width = w
    c.height = y1 - y0
    const ctx = c.getContext('2d', { willReadFrequently: true })
    ctx.fillStyle = '#fff'
    ctx.fillRect(0, 0, w, c.height)
    ctx.drawImage(img, 0, y0, w, y1 - y0, 0, 0, w, y1 - y0)
    const blob = await toBlob(c)
    if (!blob) continue
    onProgress?.(-1, `整图识别不全，分段重识别 ${i + 1}/${n}…`)
    const { data } = await worker.recognize(blob)
    if (data?.text?.trim()) texts.push(data.text.trim())
  }
  return texts.join('\n')
}

// 字段完整度：订单号/时间权重最高，缺这两项就说明整图识别不全
function completeness(f) {
  let s = 0
  if (f.order_no) s += 3
  if (f.deal_time) s += 3
  if (f.counterparty) s += 2
  if (f.product) s += 1
  if (f.price > 0) s += 1
  return s
}

async function parseAndSalvage(text) {
  const fields = await api.parseText(text).catch(() => ({}))
  return { fields, merged: salvageFields(text, fields) }
}

/**
 * 本地识别一张截图：
 * 1) 浏览器内 WASM OCR 出纯文本
 * 2) 交给后端规则解析成结构化字段
 * 3) 前端从原文兜底补齐关键字段（不依赖后端一定是最新版）
 * 4) 若订单号/时间缺失（长截图下半部分被漏识别），自动分段重识别并取更全的结果
 * @returns {Promise<{fields, rawText, salvaged: string[], sliced: boolean}>}
 */
export async function localRecognize(file, onProgress) {
  onProgress?.(-1, '加载本地 OCR 引擎…')
  const worker = await getWorker(onProgress)
  onProgress?.(-1, '识别中…')
  const { data } = await worker.recognize(file)
  let rawText = data?.text || ''
  if (!rawText.trim()) {
    throw new Error('本地 OCR 未识别出文字，可换「视觉大模型」引擎或手动填写')
  }
  onProgress?.(-1, '解析字段…')
  let best = await parseAndSalvage(rawText)
  let sliced = false

  // 长截图特征：整图只识别出上半部分，导致订单号/时间缺失 → 分段重识别
  if (completeness(best.merged) < 8) {
    let segText = ''
    try { segText = await recognizeSliced(worker, file, onProgress) } catch (e) { /* 降级用原结果 */ }
    if (segText && segText.length > 50) {
      sliced = true
      const seg = await parseAndSalvage(segText)
      // 规则解析用分段文本（顺序完整）；兜底用整图+分段并集文本（信息最全）
      const comboText = rawText + '\n' + segText
      const merged = salvageFields(comboText, seg.fields)
      if (completeness(merged) > completeness(best.merged)) {
        best = { fields: seg.fields, merged }
        rawText = comboText
      }
    }
  }

  // 仍缺「精确」时间（没有，或只有交易号推断的日期）时：
  // 对时间值所在的下半区域做一次定向识别——浅灰小字只有单独裁出放大才认得出。
  if (!best.merged.deal_time || best.merged.deal_time_approx) {
    try {
      const tText = await recognizeTimeRegion(worker, file, onProgress)
      if (tText && tText.length > 10) {
        rawText += '\n' + tText
        const tFields = await api.parseText(tText).catch(() => ({}))
        const fromRaw = salvageFields(tText, {}).deal_time
        const candidate = tFields.deal_time || fromRaw
        // 只接受带真实时分秒的时间（00:00:00 是交易号推断的占位，不算精确）
        if (candidate && !/ 00:00:00$/.test(candidate)) {
          best.merged.deal_time = candidate
          delete best.merged.deal_time_approx
        }
      }
    } catch (e) { /* 交给交易号兜底（保留 approx 标记） */ }
  }

  const salvaged = ['order_no', 'deal_time', 'counterparty'].filter(
    (k) => !best.fields?.[k] && best.merged[k]
  )
  return { fields: best.merged, rawText, salvaged, sliced }
}

export async function releaseOCR() {
  if (workerPromise) {
    try { (await workerPromise).terminate() } catch (e) { /* ignore */ }
    workerPromise = null
  }
}
