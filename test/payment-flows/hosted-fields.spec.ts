import { test, expect, type Page } from '@playwright/test';

const CHECKOUT_URL = process.env.CHECKOUT_URL || '';

const CARDS: Record<string, { name: string; number: string; exp: string; cvv: string; pin: string; otp?: string }> = {
  visa: { name: 'VISA (no OTP)', number: '4000000000002503', exp: '03/50', cvv: '11', pin: '1111' },
  mc: { name: 'Mastercard (OTP)', number: '5123450000000008', exp: '01/39', cvv: '100', pin: '1111', otp: '123456' },
};

const CARD = (process.env.CARD || 'visa').toLowerCase();

function secureInput(page: Page, containerId: string) {
  return page.frameLocator(`#${containerId} iframe`).locator('input').first();
}

async function fillField(page: Page, containerId: string, value: string) {
  const input = secureInput(page, containerId);
  await input.waitFor({ state: 'visible', timeout: 30_000 });
  await input.click();
  await input.fill(value);
}

async function settle(page: Page, ms: number): Promise<'redirect' | 'otp' | 'unknown'> {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (page.url().includes('/payments/return')) return 'redirect';
    let otp = false;
    try {
      otp = await page.locator('#otp-page').evaluate((el) => !el.hidden);
    } catch {
      otp = false;
    }
    if (otp) return 'otp';
    await page.waitForTimeout(500);
  }
  return 'unknown';
}

test(`hosted-fields charge completes (${CARDS[CARD].name})`, async ({ page }) => {
  test.setTimeout(150_000);
  const card = CARDS[CARD];
  expect(CHECKOUT_URL).not.toBe('');

  const pageErrors: string[] = [];
  const consoleLog: string[] = [];
  const gatewayCalls: string[] = [];
  page.on('pageerror', (e) => pageErrors.push(String(e)));
  page.on('console', (msg) => consoleLog.push(`${msg.type()}: ${msg.text()}`));
  page.on('response', async (res) => {
    if (/interswitchng|hostedfields/i.test(res.url())) {
      let body = '';
      try {
        body = (await res.text()).slice(0, 600);
      } catch {
        body = '<unreadable>';
      }
      gatewayCalls.push(`${res.status()} ${res.url()} :: ${body}`);
    }
  });

  async function diagnostics(page: Page, title: string, hfLogs: string[], gatewayCalls: string[]): Promise<string> {
    let message = '';
    try {
      message = await page.locator('#hf-message').textContent();
    } catch {
      message = '<no hf-message>';
    }
    const d = [
      `=== ${title} ===`,
      `hf-message: ${message}`,
      `pageErrors: ${JSON.stringify(hfLogs.filter(l => l.startsWith('pageerror:')))}`,
      `console relevant: ${JSON.stringify(hfLogs.filter(l => l.includes('hosted fields')))}`,
      `gatewayCalls: ${JSON.stringify(gatewayCalls)}`,
      `final url: ${page.url()}`,
    ].join('\n');
    console.error(d);
    throw new Error(d);
  }

  await page.goto(CHECKOUT_URL, { waitUntil: 'domcontentloaded' });
  await expect(page.locator('#pay-button')).toBeVisible({ timeout: 30_000 });

  await fillField(page, 'cardNumber-container', card.number);
  await fillField(page, 'expiry-container', card.exp);
  await fillField(page, 'cvv-container', card.cvv);
  await page.locator('#pay-button').click();

  const pin = secureInput(page, 'pin-container');
  await pin.waitFor({ state: 'visible', timeout: 30_000 });
  await pin.click();
  await pin.fill(card.pin);
  await page.locator('#continue-button').click();

  const state = await settle(page, 45_000);
  if (state === 'otp') {
    if (!card.otp) {
      throw new Error(await diagnostics(page, `unexpected OTP step for a no-OTP card`, consoleLog, gatewayCalls));
    }
    const otp = secureInput(page, 'otp-container');
    await otp.waitFor({ state: 'visible', timeout: 30_000 });
    await otp.click();
    await otp.fill(card.otp);
    await page.locator('#validate-button').click();
  }
  if (state !== 'redirect') {
    throw new Error(await diagnostics(page, `stopped at ${state}`, consoleLog, gatewayCalls));
  }

  await expect(page).toHaveURL(/\/receipts\//, { timeout: 60_000 });
  await expect(page.locator('h1')).toContainText('succeeded', { timeout: 60_000 });

  expect(pageErrors, 'page error events').toEqual([]);
  const gatewayErrors = gatewayCalls.filter((c) => !c.startsWith('2'));
  expect(gatewayErrors, 'gateway error responses').toEqual([]);
});