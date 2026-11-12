import { chromium, type Page } from '@playwright/test'

const BASE_URL = process.env.DASHBOARD_URL || 'http://localhost:5173'
const API_URL = process.env.API_URL || 'http://localhost:8080'
const API_KEY = process.env.API_KEY || 'dev_api_key'
const OUTPUT_DIR = process.env.OUTPUT_DIR || '../docs/screenshots'

// Optional: pin detail pages to specific records. When unset, the first
// account/transfer link on the list page and the newest run are used.
const ACCOUNT_ID = process.env.ACCOUNT_ID
const TRANSFER_ID = process.env.TRANSFER_ID
const RUN_ID = process.env.RUN_ID

async function capture(page: Page, path: string, file: string) {
  console.log(`Capturing ${path}...`)
  await page.goto(`${BASE_URL}${path}`)
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(500)
  await page.screenshot({ path: `${OUTPUT_DIR}/${file}`, fullPage: false })
  console.log(`  Saved ${file}`)
}

async function firstLink(page: Page, listPath: string, prefix: string): Promise<string | null> {
  await page.goto(`${BASE_URL}${listPath}`)
  await page.waitForLoadState('networkidle')
  const link = await page.$(`a[href^="${prefix}"]`)
  return link ? link.getAttribute('href') : null
}

async function newestRunId(): Promise<string | null> {
  const res = await fetch(`${API_URL}/v1/reconciliation/runs?limit=1`, {
    headers: { 'X-API-Key': API_KEY },
  })
  if (!res.ok) return null
  const body = (await res.json()) as { data: { id: string }[] }
  return body.data[0]?.id ?? null
}

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

  await capture(page, '/accounts', 'accounts-list.png')
  await capture(page, '/transfers', 'transfers-list.png')
  await capture(page, '/reconciliation', 'reconciliation.png')
  await capture(page, '/integrity', 'integrity.png')

  const accountPath = ACCOUNT_ID ? `/accounts/${ACCOUNT_ID}` : await firstLink(page, '/accounts', '/accounts/')
  if (accountPath) await capture(page, accountPath, 'account-detail.png')

  const transferPath = TRANSFER_ID ? `/transfers/${TRANSFER_ID}` : await firstLink(page, '/transfers', '/transfers/')
  if (transferPath) await capture(page, transferPath, 'transfer-detail.png')

  const runId = RUN_ID || (await newestRunId())
  if (runId) await capture(page, `/reconciliation/${runId}`, 'reconciliation-run.png')

  await browser.close()
  console.log('\nDone! Screenshots saved to', OUTPUT_DIR)
}

main().catch(err => {
  console.error('Error:', err.message)
  process.exit(1)
})
