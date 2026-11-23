import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const outputDir = path.resolve('qa-artifacts/input-capture-audit')
await fs.mkdir(outputDir, { recursive: true })

// 1×1 PNG，只用于验证“图片进入录入弹窗”的链路，不依赖 OCR 结果。
const png = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64'
)
const pngBase64 = png.toString('base64')
const checks = []

function mark(name) {
  checks.push(name)
}

const browser = await chromium.launch({ channel: 'msedge', headless: true })

{
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    permissions: ['clipboard-read', 'clipboard-write']
  })
  const page = await context.newPage()
  await page.goto(baseURL + '/', { waitUntil: 'networkidle' })
  await page.evaluate(async (base64) => {
    const binary = Uint8Array.from(atob(base64), (char) => char.charCodeAt(0))
    const file = new File([binary], 'pasted-order.png', { type: 'image/png' })
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': file })])
  }, pngBase64)
  await page.locator('body').click({ position: { x: 8, y: 8 } })
  await page.keyboard.press('Control+V')
  await page.locator('.modal-mask .modal-wide').waitFor()
  assert.ok((await page.locator('.modal-head').textContent()).includes('上传截图'))
  await page.screenshot({ path: path.join(outputDir, 'paste-open-modal.png'), scale: 'css' })
  mark('直接粘贴截图打开录入弹窗')
  await context.close()
}

{
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await context.newPage()
  await page.goto(baseURL + '/', { waitUntil: 'networkidle' })
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready
  })
  if (!await page.evaluate(() => !!navigator.serviceWorker.controller)) {
    await page.reload({ waitUntil: 'networkidle' })
  }
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready
  })
  const result = await page.evaluate(async (base64) => {
    const binary = Uint8Array.from(atob(base64), (char) => char.charCodeAt(0))
    const form = new FormData()
    form.append('image', new File([binary], 'shared-order.png', { type: 'image/png' }))
    const response = await fetch('/share-target', { method: 'POST', body: form })
    return { status: response.status, url: response.url }
  }, pngBase64)
  assert.ok(result.status >= 200 && result.status < 400)
  await page.goto(baseURL + '/?share=1#overview', { waitUntil: 'networkidle' })
  await page.locator('.modal-mask .modal-wide').waitFor()
  await page.screenshot({ path: path.join(outputDir, 'share-target-open-modal.png'), scale: 'css' })
  mark('PWA 分享目标打开录入弹窗')
  await context.close()
}

await browser.close()
const report = { checks }
await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify({ checks, screenshots: outputDir }, null, 2))
