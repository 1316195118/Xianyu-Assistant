import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'
import fs from 'node:fs/promises'
import path from 'node:path'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const routes = ['/', '/privacy', '/terms', '/not-a-page', '/#records', '/#groups', '/#consign', '/#settings']
const outputDir = path.resolve('qa-artifacts/link-audit')
await fs.mkdir(outputDir, { recursive: true })

const browser = await chromium.launch({ channel: 'msedge', headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const page = await context.newPage()
const links = new Map()

for (const route of routes) {
  await page.goto(baseURL + route, { waitUntil: 'networkidle' })
  await page.waitForTimeout(400)
  const found = await page.evaluate(() => [...document.querySelectorAll('a[href]')].map((anchor) => ({
    href: anchor.href,
    text: anchor.textContent.trim().slice(0, 80),
    page: location.href
  })))
  for (const link of found) {
    if (!links.has(link.href)) links.set(link.href, { ...link, sources: new Set() })
    links.get(link.href).sources.add(link.page)
  }
}

const results = []
for (const [href, link] of links) {
  const url = new URL(href)
  if (!['http:', 'https:'].includes(url.protocol)) {
    results.push({ href, kind: 'unsupported', ok: false, sources: [...link.sources] })
    continue
  }

  try {
    const response = await context.request.get(href, {
      failOnStatusCode: false,
      timeout: 15000
    })
    let anchorOK = true
    let missingAnchor = ''
    if (url.origin === new URL(baseURL).origin && url.hash) {
      const anchorPage = await context.newPage()
      await anchorPage.goto(href, { waitUntil: 'domcontentloaded' })
      await anchorPage.waitForTimeout(200)
      missingAnchor = decodeURIComponent(url.hash.slice(1))
      anchorOK = ['overview', 'records', 'groups', 'consign', 'settings'].includes(missingAnchor) ||
        await anchorPage.evaluate((id) => !!document.getElementById(id) || !!document.querySelector(`[name="${CSS.escape(id)}"]`), missingAnchor)
      await anchorPage.close()
    }
    results.push({
      href,
      kind: url.origin === new URL(baseURL).origin ? 'internal' : 'external',
      status: response.status(),
      ok: response.ok() && anchorOK,
      anchor: url.hash ? { id: missingAnchor, found: anchorOK } : null,
      sources: [...link.sources]
    })
  } catch (error) {
    results.push({
      href,
      kind: url.origin === new URL(baseURL).origin ? 'internal' : 'external',
      ok: false,
      error: error.message,
      sources: [...link.sources]
    })
  }
}

await browser.close()
const failures = results.filter((result) => !result.ok)
const report = {
  checked: results.length,
  internal: results.filter((result) => result.kind === 'internal').length,
  external: results.filter((result) => result.kind === 'external').length,
  failures,
  results
}
await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify({
  checked: report.checked,
  internal: report.internal,
  external: report.external,
  failures,
  report: path.join(outputDir, 'report.json')
}, null, 2))
