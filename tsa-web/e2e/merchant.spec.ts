import { test, expect } from '@playwright/test';
import { CODE, mockApi, PASSWORD } from './fixtures';

test('a merchant signs in on a new browser, rolls a test key and sets a webhook', async ({ page }) => {
  const api = await mockApi(page);
  await page.goto('/business/live/overview');
  await expect(page).toHaveURL(/\/signin$/);
  await page.getByLabel('Email').fill('ada@skyair.ng');
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByText('a***@skyair.ng', { exact: true })).toBeVisible();
  await page.getByLabel('Code', { exact: true }).fill(CODE);
  await page.getByRole('button', { name: 'Verify and sign in' }).click();
  await expect(page).toHaveURL(/\/business\/live\/overview$/);
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  // Live is switched on by TSA Connect after review: until then setup is not finished.
  await expect(page.getByText("Under review — finish a test payment and we'll switch you on")).toBeVisible();

  await page.getByRole('group', { name: 'Mode' }).getByRole('link', { name: 'Test', exact: true }).click();
  await expect(page).toHaveURL(/\/business\/test\/overview$/);
  await expect(page.getByText('Test mode — no real money moves', { exact: true })).toBeVisible();
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'API keys & webhooks' }).click();
  await expect(page).toHaveURL(/\/business\/test\/developers$/);

  await page.getByRole('button', { name: /create new test key/i }).click();
  const roll = page.getByRole('dialog', { name: 'Create a new test API key' });
  await roll.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await roll.getByRole('button', { name: /send code/i }).click();
  await roll.getByLabel('Code from your email').fill(CODE);
  await roll.getByRole('button', { name: /create new test key/i }).click();
  const shown = page.getByRole('dialog', { name: 'Your new test key' });
  // The mock mints the key when the confirm lands; read it only once it exists.
  await expect.poll(() => api.lastKey).toBeTruthy();
  await expect(shown.getByText(/^tsapk_test_/)).toHaveText(api.lastKey!);
  // Shown once: Escape does not close it, only Done does.
  await page.keyboard.press('Escape');
  await expect(shown).toBeVisible();
  await shown.getByRole('button', { name: 'Done' }).click();
  await expect(shown).toBeHidden();
  // The new key's prefix is read back from the server, never kept from the dialog.
  await expect(page.getByText(`${api.merchant.testKeyPrefix}••••••••••••`, { exact: true })).toBeVisible();

  await page.getByRole('button', { name: /edit webhooks/i }).click();
  await page.getByLabel('Test webhook URL').fill('https://api.skyair.ng/tsa/test-webhook');
  await page.getByRole('button', { name: /continue/i }).click();
  const save = page.getByRole('dialog', { name: 'Save webhook settings' });
  await save.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await save.getByRole('button', { name: /send code/i }).click();
  await save.getByLabel('Code from your email').fill(CODE);
  await save.getByRole('button', { name: 'Save webhooks' }).click();
  await expect(save).toBeHidden();
  await expect(page.getByText('https://api.skyair.ng/tsa/test-webhook', { exact: true })).toBeVisible();

  // PUT /endpoints replaces all three settings, against the version on screen.
  const endpoints = api.calls.find((c) => c.method === 'PUT' && c.path === '/api/pay/merchant/endpoints');
  expect(endpoints?.body).toEqual({
    webhookUrl: '', testWebhookUrl: 'https://api.skyair.ng/tsa/test-webhook', returnDomains: ['skyair.ng'], expectedVersion: 7, password: PASSWORD,
  });
  // Every call had an answer from the mock (so the flow used only real routes).
  expect(api.unhandled).toEqual([]);

  // No web storage, ever.
  const storage = await page.evaluate(async () => ({
    /* eslint-disable no-restricted-properties, no-restricted-globals -- asserting that nothing was stored */
    local: window.localStorage.length,
    session: window.sessionStorage.length,
    databases: (await indexedDB.databases()).length,
    /* eslint-enable no-restricted-properties, no-restricted-globals */
  }));
  expect(storage).toEqual({ local: 0, session: 0, databases: 0 });
});

test('the layout holds at phone width', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/business/live/payments');
  await expect(page.getByRole('heading', { name: 'Payments' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'PNR-K8Q2ZL' })).toBeVisible();
  const overflows = () => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(await overflows()).toBe(false);

  // Keys and webhooks: long key prefixes and URLs truncate inside their boxes.
  await page.goto('/business/live/developers');
  await expect(page.getByRole('heading', { name: 'API keys & webhooks' })).toBeVisible();
  await expect(page.getByText(`${api.merchant.liveKeyPrefix}••••••••••••`, { exact: true })).toBeVisible();
  await expect(page.getByText(/Live payments aren't on yet/)).toBeVisible();
  expect(await overflows()).toBe(false);

  // The public docs too: long code lines scroll inside their block, not the page.
  await page.goto('/docs');
  await expect(page.getByRole('heading', { name: 'Verify the signature' })).toBeVisible();
  expect(await overflows()).toBe(false);
  expect(api.unhandled).toEqual([]);
});
