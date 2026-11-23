const DEFAULT_IMAGE = '/og-image.png'

export const PAGE_META = {
  overview: {
    title: '经营概览｜鱼助手闲鱼售卖统计',
    description: '查看闲鱼交易利润、成交额、成本与服务费构成，实时掌握店铺经营状况，快速发现低利润订单。',
    robots: 'noindex, nofollow'
  },
  records: {
    title: '交易明细与成本管理｜鱼助手',
    description: '集中检索闲鱼买卖记录、成交价、运费和平台费用，按商品或订单号快速定位，补全成本并核对利润。',
    robots: 'noindex, nofollow'
  },
  groups: {
    title: '联单成本分摊｜鱼助手',
    description: '把多笔买进和卖出归入同一联单，自动分摊进货与运输成本，一眼看清每批商品的真实利润。',
    robots: 'noindex, nofollow'
  },
  consign: {
    title: '寄卖与代采结算｜鱼助手',
    description: '分别管理寄卖人和委托人往来，记录寄卖、代采与结算，按归属人快速查账，不让垫付和分成遗漏。',
    robots: 'noindex, nofollow'
  },
  operations: {
    title: '经营中心｜鱼助手',
    description: '集中完成经营数据体检、物流费用匹配、服务费核销、归属人对账、采购库龄与智能定价。',
    robots: 'noindex, nofollow'
  },
  settings: {
    title: '同步、备份与数据设置｜鱼助手',
    description: '配置闲鱼登录同步、本地或视觉 OCR、访问密码、自动备份和数据导入导出，让经营数据更安全可控。',
    robots: 'noindex, nofollow'
  },
  login: {
    title: '登录经营账本｜鱼助手',
    description: '登录自托管的鱼助手经营账本，查看闲鱼成交、成本分摊、寄卖往来和真实利润。',
    robots: 'noindex, nofollow'
  },
  privacy: {
    title: '隐私政策｜鱼助手闲鱼售卖统计',
    description: '了解鱼助手收集哪些经营数据、为何收集、如何保存和删除，以及闲鱼登录、OCR、备份与第三方服务的数据流向。',
    robots: 'index, follow'
  },
  terms: {
    title: '服务条款｜鱼助手闲鱼售卖统计',
    description: '查看鱼助手自托管账本的使用规则、账户安全、数据责任、知识产权、第三方服务、免责声明与争议处理。',
    robots: 'index, follow'
  },
  notFound: {
    title: '页面未找到｜鱼助手',
    description: '你访问的页面不存在或已经移动，可返回经营概览、交易明细、联单或系统设置继续使用鱼助手。',
    robots: 'noindex, nofollow'
  }
}

function setMeta(attribute, name, content) {
  let el = document.head.querySelector(`meta[${attribute}="${name}"]`)
  if (!el) {
    el = document.createElement('meta')
    el.setAttribute(attribute, name)
    document.head.appendChild(el)
  }
  el.setAttribute('content', content)
}

function setCanonical(url) {
  let el = document.head.querySelector('link[rel="canonical"]')
  if (!el) {
    el = document.createElement('link')
    el.setAttribute('rel', 'canonical')
    document.head.appendChild(el)
  }
  el.setAttribute('href', url)
}

export function publicPageForPath(pathname) {
  const path = `/${String(pathname || '/').replace(/^\/+|\/+$/g, '')}`
  if (path === '/privacy') return 'privacy'
  if (path === '/terms') return 'terms'
  if (path === '/') return ''
  return 'notFound'
}

export function applyPageMeta(key, options = {}) {
  const meta = PAGE_META[key] || PAGE_META.overview
  const canonicalPath = options.canonicalPath || location.pathname || '/'
  const canonical = new URL(canonicalPath, location.origin).href
  const imageMeta = document.head.querySelector('meta[property="og:image"]')
  const image = imageMeta?.content ? new URL(imageMeta.content, location.origin).href : new URL(DEFAULT_IMAGE, location.origin).href

  document.title = meta.title
  setMeta('name', 'description', meta.description)
  setMeta('name', 'robots', meta.robots)
  setMeta('property', 'og:title', meta.title)
  setMeta('property', 'og:description', meta.description)
  setMeta('property', 'og:url', canonical)
  setMeta('property', 'og:image', image)
  setMeta('name', 'twitter:title', meta.title)
  setMeta('name', 'twitter:description', meta.description)
  setMeta('name', 'twitter:image', image)
  setCanonical(canonical)
}
