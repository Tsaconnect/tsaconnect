import { test, expect, type Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { PRINT_COLORS } from '../src/features/business/scanToPay';
import { mockApi } from './fixtures';
import { decodePng, fraction } from './png';

async function requestPayment(page: Page, fields: { amount: string; description: string; customer?: string }) {
  await page.getByRole('button', { name: 'Request payment' }).first().click();
  const form = page.getByRole('dialog', { name: 'Request payment' });
  await form.getByLabel('Amount (₦)').fill(fields.amount);
  await form.getByLabel("What it's for").fill(fields.description);
  if (fields.customer) await form.getByLabel('Customer name (optional)').fill(fields.customer);
  await form.getByRole('button', { name: 'Create link' }).click();
  return form;
}

test('an owner requests a payment, shares the link and cancels it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const api = await mockApi(page, { signedIn: true });
  await page.goto('/business/live/links');
  await expect(page.getByRole('heading', { name: 'Payment links' })).toBeVisible();
  await expect(page.getByText('No payment links yet')).toBeVisible();

  // Live payments aren't on for this merchant yet: the form says so up front and points to test mode.
  await page.getByRole('button', { name: 'Request payment' }).first().click();
  const liveOff = page.getByRole('dialog', { name: 'Request payment' });
  await expect(liveOff.getByText(/Live payments aren't on for your business yet/)).toBeVisible();
  await expect(liveOff.getByLabel('Amount (₦)')).toHaveCount(0);
  await liveOff.getByRole('link', { name: 'Switch to test mode' }).click();
  await expect(page).toHaveURL(/\/business\/test\/links$/);
  await expect(page.getByText('Test mode — no real money moves', { exact: true })).toBeVisible();

  await requestPayment(page, { amount: '5,000', description: 'Balance for LOS-ABV, 12 Oct', customer: 'Tolu Adeyemi' });
  const ready = page.getByRole('dialog', { name: 'Your payment link is ready' });
  await expect(ready.getByRole('paragraph').filter({ hasText: /^₦5,000\.00$/ })).toBeVisible();
  // The fee is on top: the customer is charged more than was asked, and the owner gets what she asked.
  await expect(ready.getByText('Your customer pays')).toBeVisible();
  await expect(ready.locator('dd').first()).toHaveText('₦5,050.51');
  await expect(ready.locator('dd').last()).toHaveText('₦5,000.00');
  const made = api.payments[0];
  await expect(ready.getByText(made.checkoutUrl, { exact: true })).toBeVisible();
  await ready.getByRole('button', { name: 'Copy payment link' }).click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(made.checkoutUrl);
  const share = new URL((await ready.getByRole('link', { name: 'Share on WhatsApp' }).getAttribute('href'))!);
  expect(share.origin + share.pathname).toBe('https://wa.me/');
  expect(share.searchParams.get('text')).toBe(`Sky Air Ltd is requesting ₦5,050.51 for Balance for LOS-ABV, 12 Oct. Pay here: ${made.checkoutUrl}`);
  await expect(ready.getByRole('img', { name: 'QR code for the payment link' })).toBeVisible();

  // The QR card, drawn in a real browser: a 1080×1350 PNG named for the business. The
  // button is busy while the card is drawn, and keeps the focus through it.
  const download = ready.getByRole('button', { name: 'Download QR' });
  await download.evaluate((button) => {
    new MutationObserver(() => { if (button.getAttribute('aria-busy')) button.dataset.wasBusy = 'yes'; })
      .observe(button, { attributes: true, attributeFilter: ['aria-busy'] });
  });
  const [saved] = await Promise.all([
    page.waitForEvent('download'),
    download.evaluate((button: HTMLElement) => { button.focus(); button.click(); }), // the drawing starts with the tap
  ]);
  await expect(download).not.toHaveAttribute('aria-busy');
  await expect(download).toHaveAttribute('data-was-busy', 'yes');
  await expect(download).toBeFocused();
  expect(saved.suggestedFilename()).toBe('pay-sky-air-ltd-test.png');
  const card = decodePng(await readFile((await saved.path())!));
  expect([card.width, card.height]).toEqual([1080, 1350]);
  const dark = ([r, g, b]: number[]) => r < 80 && g < 80 && b < 80;
  const near = (hex: string) => ([r, g, b]: number[]) =>
    [r, g, b].every((c, i) => Math.abs(c - parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16)) < 24);
  expect(fraction(card, { x: 0, y: 0, w: 1080, h: 50 }, dark)).toBe(1); // the ink header band
  const qr = fraction(card, { x: 440, y: 670, w: 200, h: 200 }, dark); // the middle of the code
  expect(qr).toBeGreaterThan(0.25);
  expect(qr).toBeLessThan(0.75);
  expect(fraction(card, { x: 20, y: 500, w: 160, h: 500 }, dark)).toBe(0); // white beside it
  expect(fraction(card, { x: 0, y: 1240, w: 1080, h: 60 }, near(PRINT_COLORS.test))).toBeGreaterThan(0.005); // the test banner

  // One create, with an Idempotency-Key, in the mode on screen (live was never tried).
  const creates = api.calls.filter((c) => c.method === 'POST' && c.path === '/api/pay/merchant/links');
  expect(creates).toHaveLength(1);
  expect(creates[0].body).toEqual({ amount: 500_000, description: 'Balance for LOS-ABV, 12 Oct', customerName: 'Tolu Adeyemi', reference: '', expiresInDays: 7 });
  expect(creates[0].idempotencyKey).toBeTruthy();

  await ready.getByRole('button', { name: 'Done' }).click();
  const row = page.getByRole('row').filter({ hasText: 'Tolu Adeyemi' });
  await expect(row.getByText('Awaiting payment')).toBeVisible();

  // The link from the list: share it again, or cancel it.
  await row.getByRole('link', { name: 'Balance for LOS-ABV, 12 Oct' }).click();
  const drawer = page.getByRole('dialog', { name: made.reference });
  await expect(drawer.getByRole('link', { name: 'Share on WhatsApp' })).toBeVisible();
  await drawer.getByRole('button', { name: 'Cancel link' }).click();
  const confirm = page.getByRole('dialog', { name: 'Cancel this link?' });
  await expect(confirm.getByText("The link will stop working. This can't be undone.")).toBeVisible();
  await confirm.getByRole('button', { name: 'Cancel link' }).click();
  await expect(confirm).toBeHidden();
  await expect(drawer.getByText('Cancelled', { exact: true })).toBeVisible();
  await expect(drawer.getByRole('button', { name: 'Cancel link' })).toBeHidden();
  await drawer.getByRole('button', { name: 'Close' }).click();

  await page.locator('label').filter({ hasText: /^Cancelled$/ }).click(); // the status filter
  await expect(page).toHaveURL(/status=cancelled/);
  await expect(page.getByRole('row').filter({ hasText: 'Tolu Adeyemi' }).getByText('Cancelled')).toBeVisible();

  // Payments shows the same payment, tagged as a link.
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Payments' }).click();
  const paymentRow = page.getByRole('row').filter({ hasText: made.reference });
  await expect(paymentRow.getByText('Link', { exact: true })).toBeVisible();
  expect(api.unhandled).toEqual([]);

  const storage = await page.evaluate(async () => ({
    /* eslint-disable no-restricted-properties, no-restricted-globals -- asserting that nothing was stored */
    local: window.localStorage.length,
    session: window.sessionStorage.length,
    databases: (await indexedDB.databases()).length,
    /* eslint-enable no-restricted-properties, no-restricted-globals */
  }));
  expect(storage).toEqual({ local: 0, session: 0, databases: 0 });
});

test('payment links hold at phone width', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/business/test/links');
  await expect(page.getByRole('heading', { name: 'Payment links' })).toBeVisible();
  await requestPayment(page, { amount: '12500.50', description: 'Excess baggage, 2 bags on the Lagos to Abuja flight of 12 October' });
  const ready = page.getByRole('dialog', { name: 'Your payment link is ready' });
  await expect(ready.getByRole('img', { name: 'QR code for the payment link' })).toBeVisible();
  const overflows = () => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(await overflows()).toBe(false);
  await ready.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByRole('link', { name: /Excess baggage/ })).toBeVisible();
  expect(await overflows()).toBe(false);
  expect(api.unhandled).toEqual([]);
});

test('an owner makes a reusable link for the shop counter, prints its poster, pauses and replaces it', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  await page.goto('/business/test/links');
  await page.getByText('Reusable', { exact: true }).click();
  await expect(page).toHaveURL(/type=reusable/);
  await expect(page.getByText('No reusable links yet')).toBeVisible();

  await page.getByRole('button', { name: 'Request payment' }).first().click();
  const form = page.getByRole('dialog', { name: 'Request payment' });
  await expect(form.getByRole('radio', { name: /customer enters the amount/i })).toBeChecked();
  await form.getByLabel('Label').fill('Sky Air — Ikeja ticket desk');
  await form.getByLabel('Minimum (₦, optional)').fill('1,000');
  await form.getByRole('button', { name: 'Create link' }).click();

  const ready = page.getByRole('dialog', { name: 'Your reusable link is ready' });
  const link = api.openLinks[0];
  await expect(ready.getByText(link.url, { exact: true })).toBeVisible();
  await expect(ready.getByText(/From ₦1,000.00 to ₦5,000,000.00/)).toBeVisible();
  await expect(ready.getByRole('img', { name: 'QR code for the payment link' })).toBeVisible();
  const create = api.calls.find((c) => c.method === 'POST' && c.path === '/api/pay/merchant/open-links')!;
  expect(create.body).toEqual({ label: 'Sky Air — Ikeja ticket desk', minAmount: 100_000, maxAmount: 0 });
  expect(create.idempotencyKey).toBeTruthy();

  // The poster: a page of its own, laid out for A4, printed with the browser's Print.
  await ready.getByRole('link', { name: 'Print poster' }).click();
  await expect(page).toHaveURL(new RegExp(`/business/test/links/open/${link.id}/poster$`));
  const poster = page.getByRole('article', { name: 'Poster' });
  await expect(poster.getByRole('heading', { name: 'Sky Air Ltd' })).toBeVisible();
  await expect(poster.getByText('Scan to pay with TSA Connect')).toBeVisible();
  await expect(poster.getByText(link.url.replace('https://', ''), { exact: true })).toBeVisible();
  await expect(poster.getByText('Test poster — no real money moves')).toBeVisible();
  // Its fixed print colours, shared with the QR card.
  await expect(poster.getByText('Sky Air — Ikeja ticket desk')).toHaveCSS('color', 'rgb(61, 74, 73)');
  await expect(poster.getByText('Test poster — no real money moves')).toHaveCSS('border-top-color', 'rgb(107, 78, 14)');
  await expect(poster.getByRole('img', { name: 'QR code for the payment link' }).locator('..')).toHaveCSS('border-top-color', 'rgb(22, 32, 31)');
  await page.evaluate(() => {
    const w = window as unknown as { printed: number };
    w.printed = 0;
    window.print = () => { w.printed += 1; };
  });
  await page.getByRole('button', { name: 'Print' }).click();
  expect(await page.evaluate(() => (window as unknown as { printed: number }).printed)).toBe(1);
  // On paper it is exactly one A4 page — no second, blank one.
  const pdf = (await page.pdf({ preferCSSPageSize: true })).toString('latin1');
  expect(pdf.match(/\/Type\s*\/Page\b/g)).toHaveLength(1);
  expect(pdf).toMatch(/\/MediaBox\s*\[\s*0 0 59[45](\.\d+)? 84[12](\.\d+)?\s*\]/);

  // Two customers scan and pay; the link's detail shows what it has taken.
  const first = api.payThrough(link, 750_000, 'Two bags');
  api.payThrough(link, 500_000);
  await page.getByRole('link', { name: 'Back to the link' }).click();
  const drawer = page.getByRole('dialog', { name: 'Sky Air — Ikeja ticket desk' });
  await expect(drawer.getByText(/You received/)).toHaveText('You received ₦12,500.00 from 2 payments');
  // The customer's note shows as a note, beside the link's label.
  await expect(drawer.getByRole('link', { name: 'Sky Air — Ikeja ticket desk · Note: Two bags' })).toBeVisible();
  await expect(drawer.getByRole('link', { name: 'Sky Air — Ikeja ticket desk', exact: true })).toBeVisible();

  await drawer.getByRole('button', { name: 'Pause' }).click();
  await expect(drawer.getByText(/Paused\. Customers who open this link/)).toBeVisible();
  expect(link.active).toBe(false);
  await drawer.getByRole('button', { name: 'Resume' }).click();
  await expect(drawer.getByRole('button', { name: 'Pause' })).toBeVisible();

  // Edit: a maximum below the minimum is refused on the form; a good one is saved against the current version.
  await drawer.getByRole('button', { name: 'Edit' }).click();
  const edit = page.getByRole('dialog', { name: 'Edit link' });
  await expect(edit.getByLabel('Minimum (₦, optional)')).toHaveValue('1000');
  await edit.getByLabel('Maximum (₦, optional)').fill('500');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit.getByText('The maximum must be at least the minimum.')).toBeVisible();
  await edit.getByLabel('Label').fill('Sky Air — Ikeja desk');
  await edit.getByLabel('Maximum (₦, optional)').fill('50,000');
  const version = link.version;
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit).toBeHidden();
  const put = api.calls.filter((c) => c.method === 'PUT').at(-1)!;
  expect(put.body).toEqual({ label: 'Sky Air — Ikeja desk', minAmount: 100_000, maxAmount: 5_000_000, active: true, expectedVersion: version });
  await expect(page.getByRole('dialog', { name: 'Sky Air — Ikeja desk' }).getByText('From ₦1,000.00 to ₦50,000.00')).toBeVisible();

  const oldUrl = link.url;
  await page.getByRole('dialog', { name: 'Sky Air — Ikeja desk' }).getByRole('button', { name: 'Replace link' }).click();
  const confirm = page.getByRole('dialog', { name: 'Replace this link?' });
  await expect(confirm.getByText('Your printed posters will stop working. Print the new one.')).toBeVisible();
  await confirm.getByRole('button', { name: 'Replace link' }).click();
  await expect(confirm).toBeHidden();
  expect(link.url).not.toBe(oldUrl);
  await expect(page.getByRole('dialog', { name: 'Sky Air — Ikeja desk' }).getByText(link.url, { exact: true })).toBeVisible();
  await page.getByRole('dialog', { name: 'Sky Air — Ikeja desk' }).getByRole('button', { name: 'Close' }).click();
  await expect(page.getByRole('row').filter({ hasText: 'Sky Air — Ikeja desk' }).getByText('Taking payments')).toBeVisible();

  // Payments shows what came through it, tagged.
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Payments' }).click();
  await expect(page.getByRole('row').filter({ hasText: first.reference }).getByText('Reusable link')).toBeVisible();
  expect(api.unhandled).toEqual([]);

  const storage = await page.evaluate(async () => ({
    /* eslint-disable no-restricted-properties, no-restricted-globals -- asserting that nothing was stored */
    local: window.localStorage.length,
    session: window.sessionStorage.length,
    databases: (await indexedDB.databases()).length,
    /* eslint-enable no-restricted-properties, no-restricted-globals */
  }));
  expect(storage).toEqual({ local: 0, session: 0, databases: 0 });
});

test('the poster holds at phone width', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/business/test/links?type=reusable');
  await page.getByRole('button', { name: 'Request payment' }).first().click();
  const form = page.getByRole('dialog', { name: 'Request payment' });
  await form.getByLabel('Label').fill('Sky Air — Ikeja ticket desk, Allen Avenue, opposite the filling station');
  await form.getByRole('button', { name: 'Create link' }).click();
  await page.getByRole('dialog', { name: 'Your reusable link is ready' }).getByRole('link', { name: 'Print poster' }).click();
  await expect(page.getByRole('article', { name: 'Poster' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false);
  // The longest label still prints on one page.
  const pdf = (await page.pdf({ preferCSSPageSize: true })).toString('latin1');
  expect(pdf.match(/\/Type\s*\/Page\b/g)).toHaveLength(1);
  expect(api.unhandled).toEqual([]);
});
