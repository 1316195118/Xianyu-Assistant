import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import path from 'node:path'
import { chromium } from '../qa-artifacts/playwright-run/node_modules/playwright/index.mjs'

const baseURL = process.env.XY_AUDIT_URL || 'http://127.0.0.1:18089'
const outputDir = path.resolve('qa-artifacts/operations-audit')
await fs.mkdir(outputDir, { recursive: true })

const browser = await chromium.launch({ channel: 'msedge', headless: true })
const errors = []
const checks = []

function mark(name, detail = '') {
  checks.push({ name, ok: true, detail })
}

async function openOperations(viewport) {
  const context = await browser.newContext({ viewport })
  const page = await context.newPage()
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text())
  })
  await page.goto(baseURL + '/#operations', { waitUntil: 'domcontentloaded' })
  await page.locator('.ops-page').waitFor()
  await page.locator('.ops-tabs button').first().waitFor()
  return { context, page }
}

async function selectTab(page, label) {
  await page.locator('.ops-tabs button').filter({ hasText: label }).click()
  await page.locator('.ops-section').waitFor()
}

async function screenshot(page, name) {
  await page.screenshot({
    path: path.join(outputDir, name + '.png'),
    scale: 'css',
    fullPage: false
  })
}

const desktop = await openOperations({ width: 1440, height: 900 })
const { page } = desktop

const tabLabels = await page.locator('.ops-tabs button').allTextContents()
const operationTabs = ['数据体检', '待办中心', '物流费用', '服务费核销', '归属人对账', '采购批次', '定价补货', '操作审计', '质量趋势', '多店驾驶舱']
for (const label of operationTabs) {
  assert.ok(tabLabels.some((text) => text.includes(label)), `缺少经营中心页签：${label}`)
}
mark('经营中心页签完整')

await page.locator('.ops-score-num').waitFor()
const healthScore = Number(await page.locator('.ops-score-num').textContent())
assert.ok(healthScore >= 0 && healthScore <= 100)
await screenshot(page, '1440-health')
mark('数据体检可加载', `health=${healthScore}`)

await selectTab(page, '待办中心')
await page.locator('.todo-overview, .ops-empty').first().waitFor()
assert.ok((await page.locator('.todo-overview, .ops-empty').count()) > 0)
await screenshot(page, '1440-todos')
mark('待办中心可加载')

await selectTab(page, '物流费用')
await page.locator('.ops-stats > .card').first().waitFor()
assert.equal(await page.locator('.ops-stats > .card').count(), 4)
await page.locator('.transport-drop input[type=file]').setInputFiles({
  name: 'operations-audit.csv',
  mimeType: 'text/csv',
  buffer: Buffer.from(
    '订单号,运单号,快递公司,运费,时间,备注\n' +
    'SY-AUDIT-NOT-EXIST,SF-AUDIT-001,顺丰,11.50,2026-11-23 11:00:00,UI审计\n',
    'utf8'
  )
})
await page.locator('.transport-preview').waitFor()
assert.equal(await page.locator('.transport-preview b').first().textContent(), '已读取 1 条')
await page.getByLabel('同时补填到卖单运费').uncheck()
await page.getByRole('button', { name: '确认导入' }).click()
await page.locator('.transport-preview').waitFor({ state: 'detached' })
await screenshot(page, '1440-transport')
mark('物流 CSV 预览、导入闭环')

await selectTab(page, '服务费核销')
await page.locator('.ops-stats.five > .card').first().waitFor()
assert.equal(await page.locator('.ops-stats.five > .card').count(), 5)
await page.locator('.fee-table').waitFor()
await screenshot(page, '1440-fees')
mark('服务费核销列表可加载')

await selectTab(page, '归属人对账')
await page.locator('.owner-list, .ops-empty').first().waitFor()
const ownerRows = page.locator('.owner-list > button')
if (await ownerRows.count()) {
  await ownerRows.first().click()
  await page.locator('.statement-print').waitFor()
  assert.ok((await page.locator('.statement-head h2').textContent()).includes('对账单'))
  await screenshot(page, '1440-owners')
  await page.emulateMedia({ media: 'print' })
  await page.pdf({ path: path.join(outputDir, 'owner-statement-a4.pdf'), format: 'A4', printBackground: true })
  await page.emulateMedia({ media: 'screen' })
  mark('对账单可生成')
} else {
  mark('对账单空状态可加载')
}

await selectTab(page, '采购批次')
await page.locator('.lot-grid, .ops-empty').first().waitFor()
assert.ok((await page.locator('.lot-grid, .ops-empty').count()) > 0)
const editLot = page.getByRole('button', { name: '编辑批次' }).first()
if (await editLot.count()) {
  await editLot.click()
  assert.equal(await page.getByPlaceholder('批次号').count(), 1)
  await page.getByRole('button', { name: '取消' }).click()
}
await screenshot(page, '1440-lots')
mark('采购批次编辑入口可用')

await selectTab(page, '定价补货')
await page.locator('.pricing-card, .ops-empty').first().waitFor()
assert.ok((await page.locator('.pricing-card, .ops-empty').count()) > 0)
if (await page.locator('.pricing-card').count()) {
  await page.locator('input[type=range]').evaluate((input) => {
    input.value = '30'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await page.getByText('30%', { exact: true }).waitFor()
}
await screenshot(page, '1440-pricing')
mark('定价滑杆与结果可刷新')

await selectTab(page, '操作审计')
await page.locator('.audit-toolbar').waitFor()
assert.ok((await page.locator('.audit-row, .ops-empty').count()) > 0)
await page.getByLabel('搜索操作审计').fill('记录')
await page.getByRole('button', { name: '搜索', exact: true }).click()
await page.locator('.audit-row, .ops-empty').first().waitFor()
await screenshot(page, '1440-audit')
mark('操作审计可搜索')

await selectTab(page, '质量趋势')
await page.locator('.quality-chart, .quality-chart-skeleton, .ops-empty').first().waitFor()
assert.ok((await page.locator('.quality-chart, .quality-chart-skeleton, .ops-empty').count()) > 0)
await screenshot(page, '1440-quality')
mark('数据质量趋势可加载')

await selectTab(page, '多店驾驶舱')
await page.locator('.cockpit-card, .ops-empty').first().waitFor()
assert.ok((await page.locator('.cockpit-card, .ops-empty').count()) > 0)
await screenshot(page, '1440-multi')
if (await page.locator('.cockpit-card').count()) {
  await page.locator('.cockpit-card').first().click()
  await page.locator('.ops-score-num').waitFor()
}
mark('多店驾驶舱可加载')

async function homepageCards(width, height) {
  const context = await browser.newContext({ viewport: { width, height } })
  const home = await context.newPage()
  await home.goto(baseURL + '/', { waitUntil: 'domcontentloaded' })
  await home.locator('.hero-card').waitFor()
  await home.locator('.kpi-card').first().waitFor()
  const result = await home.evaluate(() => {
    const metric = (selector) => {
      const card = document.querySelector(selector)
      const value = card.querySelector('.hero-value, .kpi-value')
      const style = getComputedStyle(card)
      return {
        padding: style.padding,
        valueFontSize: getComputedStyle(value).fontSize,
        height: Math.round(card.getBoundingClientRect().height * 10) / 10
      }
    }
    return {
      hero: metric('.hero-card'),
      sales: metric('.kpi-card')
    }
  })
  await home.screenshot({ path: path.join(outputDir, `${width}x${height}-home.png`), scale: 'css' })
  await context.close()
  return result
}

const hdCards = await homepageCards(1440, 900)
const uhdCards = await homepageCards(3840, 2160)
assert.equal(uhdCards.hero.padding, hdCards.hero.padding)
assert.equal(uhdCards.sales.padding, hdCards.sales.padding)
assert.ok(parseFloat(uhdCards.hero.valueFontSize) <= 39, '4K 首屏数字字号超过设计上限')
assert.ok(parseFloat(uhdCards.sales.valueFontSize) <= 22, '4K 销售额字号超过设计上限')
assert.ok(uhdCards.hero.height - hdCards.hero.height <= 10, '4K 首屏卡片被额外撑高')
assert.ok(uhdCards.sales.height - hdCards.sales.height <= 12, '4K 销售额卡片被额外撑高')
mark('4K 卡片尺寸受控', JSON.stringify({ hdCards, uhdCards }))

const mobile = await openOperations({ width: 375, height: 812 })
for (const label of operationTabs) {
  await selectTab(mobile.page, label)
  const metrics = await mobile.page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth
  }))
  assert.equal(metrics.scrollWidth, metrics.clientWidth, `${label} 在 375px 下横向溢出`)
}
await screenshot(mobile.page, '375-pricing')
mark('375px 全部经营页签无横向溢出')

await desktop.context.close()
await mobile.context.close()
await browser.close()

const report = { checks, errors }
await fs.writeFile(path.join(outputDir, 'report.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify({
  checks: checks.length,
  errors,
  report: path.join(outputDir, 'report.json'),
  screenshots: outputDir
}, null, 2))
