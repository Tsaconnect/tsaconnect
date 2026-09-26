import { test, expect, type Page } from '@playwright/test';
import { mockApi } from './fixtures';

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
