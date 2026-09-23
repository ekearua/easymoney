// Drives the live /admin/media-report page and screenshots it: the per-day
// AI-token trend sparkline, the per-channel table, and the per-day table.
// MEDIA_REPORT_META_FILE = "<base>\n<session-cookie>" written by the harness.
const { chromium } = require('playwright-core');
const fs = require('fs');

const META = process.env.MEDIAREPORT_META;
const OUT = process.env.MEDIAREPORT_OUT || '.';

(async () => {
  const [base, cookie] = fs.readFileSync(META, 'utf8').trim().split('\n');
  if (!base || !cookie) { console.error('meta file must hold base URL + cookie'); process.exit(2); }

  const browser = await chromium.launch({
    executablePath: 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
    headless: true,
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  await ctx.addCookies([{ name: 'wpd_admin', value: cookie, url: base }]);

  const page = await ctx.newPage();
  const errors = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', (e) => errors.push(String(e)));

  let pass = 0, fail = 0;
  const ok = (cond, name) => { if (cond) { pass++; console.log('PASS', name); } else { fail++; console.log('FAIL', name); } };

  await page.goto(base + '/admin/media-report', { waitUntil: 'load' });

  ok((await page.title()).includes('Channel media'), 'page title renders');
  ok(await page.locator('svg.token-trend').count() === 1, 'token trend sparkline renders exactly once');
  const bars = await page.locator('svg.token-trend rect.tt-bar').count();
  ok(bars === 8, `sparkline has 8 day bars (got ${bars})`);
  const spikes = await page.locator('svg.token-trend rect.tt-spike').count();
  ok(spikes >= 1, `spike day highlighted (got ${spikes} spike bars)`);
  const peak = await page.locator('svg.token-trend line.tt-peak').count();
  ok(peak === 1, 'peak reference line renders');
  ok(await page.locator('text=AI tokens per day').count() === 1, 'trend section header renders');

  // Per-channel table: seeded channels present with correct totals.
  const body = await page.locator('body').innerText();
  for (const ch of ['whatsapp', 'telegram', 'instagram', 'tiktok', 'web']) {
    ok(body.includes(ch), `channel row present: ${ch}`);
  }
  ok(body.includes('4,493') || body.includes('4493'), `total tokens renders (got in body)`);
  ok(await page.locator('table').count() >= 2, 'per-channel and per-day tables render');

  await page.screenshot({ path: OUT + '/media-report-top.png' });
  await page.locator('h2:text-is("Per channel · last 7 days")').scrollIntoViewIfNeeded();
  await page.screenshot({ path: OUT + '/media-report-channels.png' });
  await page.locator('h2:text-is("Per day")').scrollIntoViewIfNeeded();
  await page.screenshot({ path: OUT + '/media-report-days.png' });

  // Trend bars must not overlap the section below (visual sanity at width).
  const svgBox = await page.locator('svg.token-trend').boundingBox();
  ok(svgBox && svgBox.height > 20 && svgBox.height < 300, `sparkline sized sanely (h=${svgBox && svgBox.height})`);

  ok(errors.length === 0, `no console/page errors (${errors.length})`);
  if (errors.length) console.log(errors.slice(0, 5));

  console.log(`\n${pass} passed, ${fail} failed`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
