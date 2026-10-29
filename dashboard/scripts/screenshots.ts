import { chromium } from '@playwright/test'

const BASE_URL = process.env.DASHBOARD_URL || 'http://localhost:5173'
const API_KEY = process.env.API_KEY || 'dev_api_key'
const OUTPUT_DIR = process.env.OUTPUT_DIR || '../docs/screenshots'

async function main() {
  const browser = await chromium.launch()
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
  })
  const page = await context.newPage()

  // Set API key in localStorage
  await page.goto(BASE_URL)
  await page.evaluate((key) => {
    localStorage.setItem('apiKey', key)
  }, API_KEY)

  const pages = [
    { path: '/accounts', file: 'accounts-list.png', wait: 2000 },
    { path: '/transfers', file: 'transfers-list.png', wait: 2000 },
    { path: '/reconciliation', file: 'reconciliation.png', wait: 2000 },
    { path: '/integrity', file: 'integrity.png', wait: 2000 },
  ]

  for (const p of pages) {
    console.log(`Capturing ${p.path}...`)
    await page.goto(`${BASE_URL}${p.path}`)
    await page.waitForTimeout(p.wait)
    await page.screenshot({ path: `${OUTPUT_DIR}/${p.file}`, fullPage: false })
    console.log(`  Saved ${p.file}`)
  }

  // Try to capture detail pages if data exists
  await page.goto(`${BASE_URL}/accounts`)
  await page.waitForTimeout(1000)
  const firstAccountLink = await page.$('a[href^="/accounts/"]')
  if (firstAccountLink) {
    const href = await firstAccountLink.getAttribute('href')
    if (href) {
      console.log(`Capturing ${href}...`)
      await page.goto(`${BASE_URL}${href}`)
      await page.waitForTimeout(2000)
      await page.screenshot({ path: `${OUTPUT_DIR}/account-detail.png`, fullPage: false })
      console.log('  Saved account-detail.png')
    }
  }

  await page.goto(`${BASE_URL}/transfers`)
  await page.waitForTimeout(1000)
  const firstTransferLink = await page.$('a[href^="/transfers/"]')
  if (firstTransferLink) {
    const href = await firstTransferLink.getAttribute('href')
    if (href) {
      console.log(`Capturing ${href}...`)
      await page.goto(`${BASE_URL}${href}`)
      await page.waitForTimeout(2000)
      await page.screenshot({ path: `${OUTPUT_DIR}/transfer-detail.png`, fullPage: false })
      console.log('  Saved transfer-detail.png')
    }
  }

  await browser.close()
  console.log('\nDone! Screenshots saved to', OUTPUT_DIR)
}

main().catch(err => {
  console.error('Error:', err.message)
  process.exit(1)
})
