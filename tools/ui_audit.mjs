import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'
import fs from 'node:fs/promises'
import path from 'node:path'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const outputDir = path.resolve('qa-artifacts/launch-audit')
await fs.mkdir(outputDir, { recursive: true })

const browser = await chromium.launch({ channel: 'msedge', headless: true })
const errors = []
const results = []

async function auditViewport(width, height, routes) {
  const context = await browser.newContext({
    viewport: { width, height },
    isMobile: width <= 430,
    hasTouch: width <= 430
  })
  const page = await context.newPage()
  page.on('pageerror', (error) => errors.push({ width, error: error.message }))
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    // 404 页故意返回 HTTP 404，浏览器会记一条资源加载错误；这不是页面脚本异常。
    if (page.url().endsWith('/not-a-page') && /Failed to load resource.*404/i.test(message.text())) return
    errors.push({ width, error: message.text() })
  })

  for (const route of routes) {
    await page.goto(baseURL + route, { waitUntil: 'networkidle' })
    await page.waitForTimeout(500)
    const metrics = await page.evaluate(() => {
      const root = document.documentElement
      const body = document.body
      const visible = (el) => {
        const style = getComputedStyle(el)
        const rect = el.getBoundingClientRect()
        return style.display !== 'none' && style.visibility !== 'hidden' && rect.width > 0 && rect.height > 0
      }
      const smallTargets = [...document.querySelectorAll('a[href], button, input:not([type="hidden"]), select, textarea, [role="button"]')]
        .filter(visible)
        // HelpTip 的视觉图标保持紧凑，但 ::after 已把实际点击区域扩展到 44×44。
        .filter((el) => !el.classList.contains('help-tip'))
        .map((el) => {
          const rect = el.getBoundingClientRect()
          return {
            tag: el.tagName.toLowerCase(),
            className: typeof el.className === 'string' ? el.className : '',
            text: (el.getAttribute('aria-label') || el.textContent || el.getAttribute('placeholder') || '').trim().slice(0, 60),
            width: Math.round(rect.width),
            height: Math.round(rect.height)
          }
        })
        .filter((item) => item.width < 44 || item.height < 44)
      const images = [...document.images].map((img) => ({
        src: img.getAttribute('src') || '',
        alt: img.getAttribute('alt'),
        width: img.getAttribute('width'),
        height: img.getAttribute('height'),
        loading: img.getAttribute('loading'),
        complete: img.complete,
        naturalWidth: img.naturalWidth,
        naturalHeight: img.naturalHeight
      }))
      return {
        title: document.title,
        description: document.querySelector('meta[name="description"]')?.content || '',
        canonical: document.querySelector('link[rel="canonical"]')?.href || '',
        robots: document.querySelector('meta[name="robots"]')?.content || '',
        viewport: { width: innerWidth, height: innerHeight },
        document: {
          clientWidth: root.clientWidth,
          scrollWidth: root.scrollWidth,
          bodyScrollWidth: body.scrollWidth,
          scrollHeight: root.scrollHeight
        },
        overflowX: root.scrollWidth > root.clientWidth,
        smallTargets: smallTargets.slice(0, 20),
        images
      }
    })
    const key = route === '/' ? 'home' : route.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '')
    const imageName = `${width}x${height}-${key || 'home'}.png`
    await page.screenshot({ path: path.join(outputDir, imageName), scale: 'css' })
    results.push({ route, width, height, screenshot: imageName, ...metrics })
  }

  await context.close()
}

const publicRoutes = ['/', '/privacy', '/terms', '/not-a-page']
const appRoutes = ['/#records', '/#groups', '/#consign', '/#settings']

await auditViewport(1440, 900, [...publicRoutes, ...appRoutes])
await auditViewport(768, 1024, [...publicRoutes, ...appRoutes])
await auditViewport(390, 844, [...publicRoutes, ...appRoutes])
await auditViewport(375, 812, [...publicRoutes, ...appRoutes])
await auditViewport(3840, 2160, ['/', '/privacy', '/#records', '/#settings'])

await browser.close()
await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify({ results, errors }, null, 2))

const failures = results.filter((item) => item.overflowX)
const imageFailures = results.flatMap((item) =>
  item.images
    .filter((img) => img.alt == null || /^(image|img|图片|截图)$/i.test(img.alt))
    .map((img) => ({ route: item.route, width: item.width, ...img }))
)
const brokenImages = results.flatMap((item) =>
  item.images
    .filter((img) => !img.complete || img.naturalWidth === 0 || img.naturalHeight === 0)
    .map((img) => ({ route: item.route, width: item.width, ...img }))
)

console.log(JSON.stringify({
  pagesChecked: results.length,
  horizontalOverflows: failures.map((item) => ({ route: item.route, width: item.width, scrollWidth: item.document.scrollWidth })),
  imageAltFailures: imageFailures,
  brokenImages,
  javascriptErrors: errors,
  screenshots: outputDir
}, null, 2))
