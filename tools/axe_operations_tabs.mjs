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
const tabs = ['数据体检', '物流费用', '服务费核销', '归属人对账', '采购批次', '定价补货', '多店驾驶舱']

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
    await page.goto(baseURL + '/#operations', { waitUntil: 'networkidle' })
    await page.locator('.ops-page').waitFor()

    for (const tab of tabs) {
      await page.locator('.ops-tabs button').filter({ hasText: tab }).click()
      await page.locator('.ops-section').waitFor()
      await page.waitForTimeout(500)
      await page.addStyleTag({ content: '*,*::before,*::after{animation:none!important;transition:none!important}' })
      const analysis = await new AxeBuilder({ page })
        .include('.ops-page')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
        .analyze()
      results.push({
        viewport,
        theme,
        tab,
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
