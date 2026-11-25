import AxeBuilder from '../qa-artifacts/playwright-run/node_modules/@axe-core/playwright/dist/index.js'
import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'
import fs from 'node:fs/promises'
import path from 'node:path'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const outputDir = path.resolve('qa-artifacts/axe-audit')
const routes = ['/', '/privacy', '/terms', '/not-a-page', '/#records', '/#groups', '/#consign', '/#operations', '/#settings']
const viewports = [
  { width: 1440, height: 900 },
  { width: 768, height: 1024 },
  { width: 390, height: 844 },
  { width: 375, height: 812 }
]
const themes = ['light', 'dark']

await fs.mkdir(outputDir, { recursive: true })
const browser = await chromium.launch({ channel: 'msedge', headless: true })
const results = []

for (const viewport of viewports) {
  for (const theme of themes) {
  const context = await browser.newContext({
    viewport,
    isMobile: viewport.width <= 430,
    hasTouch: viewport.width <= 430
  })
  await context.addInitScript((mode) => localStorage.setItem('xy-theme', mode), theme)
  const page = await context.newPage()

  for (const route of routes) {
    process.stderr.write(`[axe] ${viewport.width}px ${theme} ${route}\n`)
    await page.goto(baseURL + route, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(700)
    await page.addStyleTag({ content: '*,*::before,*::after{animation:none!important;transition:none!important}' })
    await page.evaluate(() => {
      for (const animation of document.getAnimations()) {
        try { animation.finish() } catch (error) { /* 无限动画无需强制结束 */ }
      }
    })

    const analysis = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze()

    results.push({
      viewport,
      theme,
      route,
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
  byImpact: results.reduce((counts, item) => {
    for (const violation of item.violations) {
      counts[violation.impact || 'unknown'] = (counts[violation.impact || 'unknown'] || 0) + 1
    }
    return counts
  }, {}),
  rules: [...new Set(results.flatMap((item) => item.violations.map((violation) => violation.id)))],
  results
}

await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify(summary, null, 2))
console.log(JSON.stringify({
  checked: summary.checked,
  violations: summary.violations,
  byImpact: summary.byImpact,
  rules: summary.rules,
  report: path.join(outputDir, 'report.json')
}, null, 2))
