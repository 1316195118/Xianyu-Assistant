// 纯函数：从 OCR 原文兜底补全关键文本字段，无任何依赖，可单独测试。
//
// 背景：后端 /api/parse-text 的规则再全，也可能因为部署版本不一致、缓存、
// 反代指向旧实例、或个别 OCR 变体而漏掉字段。把「订单号 / 交易时间 / 对方昵称」
// 这三个最影响使用的字段在前端也提取一遍，作为最后一道防线——只要 OCR 原文
// 里有这些文字，表单就一定能填上，不再依赖后端一定是最新版。
export function salvageFields(raw, fields) {
  const out = { ...(fields || {}) }
  if (!raw) return out
  const flat = raw.replace(/[ \t]+/g, ' ')
  // 后端也可能返回「交易号推断」的 00:00:00 占位时间：视为未取到精确时间，
  // 让下面重新尝试提取真实时间戳，并正确标记 approx 供 UI 提示 / 定向重识别。
  if (/ 00:00:00$/.test(out.deal_time || '')) {
    out.deal_time = ''
  }

  // 1) 订单号：连续数字串里长度 15~20 位的那个。
  //    支付宝交易号约 28 位、手机号 11 位、金额带小数点，都会被天然排除。
  if (!out.order_no) {
    const nums = flat.match(/\d{6,}/g) || []
    const hit = nums.find(n => n.length >= 15 && n.length <= 20)
    if (hit) out.order_no = hit
  }

  // 2) 交易时间：买进单取第一个（下单时间），卖出单取最后一个（成交时间）。
  //    兼容 OCR 把 '-' 识别成 '.'/'/'/'年'、':' 前后空格等情况。
  if (!out.deal_time) {
    const re = /(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})\s*(\d{1,2}):(\d{2}):(\d{2})/g
    const all = [...flat.matchAll(re)]
    if (all.length) {
      const m = out.side === 'sell' ? all[all.length - 1] : all[0]
      const p2 = (x) => String(x).padStart(2, '0')
      out.deal_time = `${m[1]}-${p2(m[2])}-${p2(m[3])} ${p2(m[4])}:${m[5]}:${m[6]}`
    }
  }

  // 3) 对方昵称：标签后的词。标签本身容错「昵/呢/尼/伲」及中间噪字。
  //    只吃空格不吃换行：若 OCR 把昵称行整行漏掉、标签后直接换行到「下单时间」，
  //    这里会匹配失败而留空，绝不把时间标签当昵称。
  if (!out.counterparty) {
    const m = raw.match(/(?:卖|买)\s*家[\s\S]{0,8}?[昵呢尼伲][ \t]*称[ \t]*([^\s，,。|~～\n]{1,24})/)
    if (m) {
      let cp = m[1].replace(/[，,。|~～]+$/, '').replace(/\d+$/, '')
      if (cp && !/时间|日期|编号|地址|交易|复制|付款|发货|成交|下单/.test(cp)) {
        out.counterparty = cp
      }
    }
  }

  // 4) 时间终极兜底：手机端长截图 OCR 常把「下单时间」右侧的浅灰数值整片漏掉，
  //    但「支付宝交易号」那串长数字通常能识别到。其格式固定 yyyyMMdd + 20 余位，
  //    前 8 位即下单日期。用它还原出准确日期（时分秒无法还原，置 00:00:00 并标记）。
  //    严格限定 26~30 位且以 20 开头，避免把订单号/手机号误当交易号。
  if (!out.deal_time) {
    const longs = flat.match(/\d{20,32}/g) || []
    const txn = longs.find(n => n.length >= 26 && n.length <= 30 && n.startsWith('20'))
    if (txn) {
      const y = txn.slice(0, 4), mo = txn.slice(4, 6), d = txn.slice(6, 8)
      if (+mo >= 1 && +mo <= 12 && +d >= 1 && +d <= 31) {
        out.deal_time = `${y}-${mo}-${d} 00:00:00`
        out.deal_time_approx = true   // 前端可提示「仅日期，来源支付宝交易号」
      }
    }
  }

  return out
}
