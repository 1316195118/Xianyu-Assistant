import AxeBuilder from '../qa-artifacts/playwright-run/node_modules/@axe-core/playwright/dist/index.js'
import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'
import fs from 'node:fs/promises'
import path from 'node:path'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const outputDir = path.resolve('qa-artifacts/axe-operations-tabs')
const viewports = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 }
]
const tabs = [
  { label: '数据体检', ready: '.health-list, .ops-empty, .ops-skeleton' },
  { label: '待办中心', ready: '.todo-overview, .ops-empty, .ops-skeleton' },
  { label: '物流费用', ready: '.transport-import' },
  { label: '服务费核销', ready: '.ops-stats.five, .ops-skeleton, .ops-empty' },
  { label: '归属人对账', ready: '.owner-layout' },
  { label: '采购批次', ready: '.lot-grid, .ops-empty, .ops-skeleton' },
  { label: '定价补货', ready: '.pricing-grid, .ops-empty, .ops-skeleton' },
  { label: '操作审计', ready: '.audit-toolbar' },
  { label: '质量趋势', ready: '.quality-chart, .ops-empty, .ops-skeleton' },
  { label: '多店驾驶舱', ready: '.shop-cockpit, .ops-empty, .ops-skeleton' }
]

await fs.mkdir(outputDir, { recursive: true })
const browser = await chromium.launch({ channel: 'msedge', headless: true })
const results = []

for (const viewport of viewports) {
  for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({
      viewport,
      isMobile: viewport.width <= 430,
      hasTouch: viewport.width <= 430
    })
    await context.addInitScript((mode) => localStorage.setItem('xy-theme', mode), theme)
    const page = await context.newPage()
    await page.goto(baseURL + '/#operations', { waitUntil: 'domcontentloaded' })
    await page.locator('.ops-page').waitFor()

    for (const item of tabs) {
      process.stderr.write(`[axe] ${viewport.width}px ${theme} ${item.label}\n`)
      await page.locator('.ops-tabs button').filter({ hasText: item.label }).click()
      await page.locator(item.ready).first().waitFor({ state: 'visible', timeout: 15000 })
      await page.waitForTimeout(350)
      const analysis = await new AxeBuilder({ page })
        .include('.ops-page')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
        .options({ preload: false })
        .analyze()
      results.push({
        viewport,
        theme,
        tab: item.label,
        violations: analysis.violations.map((violation) => ({
          id: violation.id,
          impact: violation.impact,
          help: violation.help,
          nodes: violation.nodes.map((node) => ({
            target: node.target,
            html: node.html,
            failureSummary: node.failureSummary
          }))
        }))
      })
    }
    await context.close()
  }
}

await browser.close()
const summary = {
  checked: results.length,
  violations: results.reduce((total, item) => total + item.violations.length, 0),
  rules: [...new Set(results.flatMap((item) => item.violations.map((violation) => violation.id)))],
  results
}
await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify(summary, null, 2))
console.log(JSON.stringify({
  checked: summary.checked,
  violations: summary.violations,
  rules: summary.rules,
  report: path.join(outputDir, 'report.json')
}, null, 2))
