import { test, expect, type Page } from '@playwright/test';
import { CODE, mockApi, PASSWORD } from './fixtures';

const waitingNote = "Your new name 'Sky Air Nigeria' is waiting for approval. Customers and payouts keep 'Sky Air Ltd' until then.";

async function askFor(page: Page, name: string, title: string) {
  await page.getByRole('region', { name: 'Business name' }).getByRole('button', { name: 'Change business name' }).click();
  const edit = page.getByRole('dialog', { name: 'Change your business name' });
  await expect(edit.getByLabel('New business name')).toBeFocused();
  await edit.getByLabel('New business name').fill(name);
  await edit.getByRole('button', { name: 'Continue' }).click();
  const guard = page.getByRole('dialog', { name: title });
  await guard.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await guard.getByRole('button', { name: /send code/i }).click();
  return guard;
}

test('an owner asks for a new business name: it waits for approval, and can be withdrawn', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  await page.goto('/business/live/payouts');
  const card = page.getByRole('region', { name: 'Business name' });
  await expect(card.getByText('Sky Air Ltd', { exact: true })).toBeVisible();

  // A reserved word is refused by the API, and shown on the name field.
  await askFor(page, 'Sky Air Support', 'Confirm your new business name');
  const edit = page.getByRole('dialog', { name: 'Change your business name' });
  await expect(edit.getByText('A business name can\'t contain "support"')).toBeVisible();
  await expect(edit.getByLabel('New business name')).toHaveValue('Sky Air Support');
  await edit.getByRole('button', { name: 'Cancel' }).click();

  const guard = await askFor(page, 'Sky Air Nigeria', 'Confirm your new business name');
  await guard.getByLabel('Code from your email').fill(CODE);
  await guard.getByRole('button', { name: 'Send for approval' }).click();
  await expect(guard).toBeHidden();
  await expect(card.getByText(/is waiting for approval/)).toContainText(waitingNote);
  // Nothing is renamed until an admin approves it.
  await expect(card.getByText('Sky Air Ltd', { exact: true })).toBeVisible();
  expect(api.merchant.businessName).toBe('Sky Air Ltd');
  const rename = api.calls.filter((c) => c.method === 'PUT' && c.path === '/api/pay/merchant/business-name').at(-1);
  expect(rename?.body).toEqual({ name: 'Sky Air Nigeria', password: PASSWORD, expectedVersion: 7 });

  // The current name back withdraws it: the API answers without a code, and the dialog finishes.
  const keep = await askFor(page, 'Sky Air Ltd', 'Keep your business name');
  await expect(keep).toBeHidden();
  await expect(card.getByText(/is waiting for approval/)).toHaveCount(0);

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Overview' }).click();
  await expect(page.getByText('Sky Air Ltd · live payments')).toBeVisible();
  expect(api.unhandled).toEqual([]);
});

test('the business name and the account hint hold at phone width', async ({ page }) => {
  const api = await mockApi(page, { signedIn: true });
  Object.assign(api.merchant, { pendingBusinessName: 'Sky Air Nigeria Aviation Services Limited', pendingSince: new Date().toISOString() });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/business/live/payouts');
  const card = page.getByRole('region', { name: 'Business name' });
  await expect(card.getByText(/is waiting for approval/)).toBeVisible();
  const overflows = () => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(await overflows()).toBe(false);

  // A verified name is the one on the ID: the web only points to the app to change it.
  await page.goto('/account');
  await expect(page.getByText("To change your name, use the TSA Connect app — you'll verify your identity again.")).toBeVisible();
  expect(await overflows()).toBe(false);
  expect(api.unhandled).toEqual([]);
});
