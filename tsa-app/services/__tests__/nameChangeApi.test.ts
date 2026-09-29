/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * The name-change client: each call goes to its route (the overview carries
 * the device wallet), a start carries the screen's own Idempotency-Key with
 * the payment choice and the password — no wallet, no signature — confirm and cancel
 * answer { change } and are unwrapped, start-verification answers the whole
 * session, and a refusal keeps what it carries (the blockers, the next date).
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { API_BASE_URL } from '@/constants/api/config';
import * as walletApi from '../walletApi';
import {
  NameChangeError, canCancelNameChange, cancelNameChange, confirmNameChangePayment, getNameChange,
  parseFeeUsd, startNameChange, startNameChangeVerification,
} from '../nameChangeApi';

const BASE = `${API_BASE_URL}/users/me/name-change`;
const WALLET = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ success: true, data }) });
const refused = (status: number, body: unknown) => ({ ok: false, status, json: async () => body });
const call = (i = 0) => (global.fetch as jest.Mock).mock.calls[i] as [string, RequestInit & { headers: Record<string, string> }];
const change = { id: 'nc1', status: 'paid' };
const mcgpPay = { payMethod: 'mcgp_spendable' as const, password: 'pw' };

beforeEach(() => { (global as any).fetch = jest.fn(async () => ok({ change })); });

it('reads the overview for the device wallet', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ enabled: true, change: null, quote: null }));
  await getNameChange(WALLET);
  expect(call(0)[0]).toBe(`${BASE}?walletAddress=${WALLET}`);
  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ enabled: true }));
  await getNameChange(null);
  expect(call(1)[0]).toBe(BASE);
});

it('confirm and cancel answer { change } — unwrapped; start-verification answers the whole session', async () => {
  await expect(confirmNameChangePayment('0xabc')).resolves.toEqual(change);
  expect(call(0)[0]).toBe(`${BASE}/confirm-payment`);
  expect(JSON.parse(String(call(0)[1].body))).toEqual({ txHash: '0xabc' });

  const session = { url: 'https://verify.didit.me/s/1', sessionId: 's1', resumed: false, attempts: 1, maxAttempts: 3 };
  (global.fetch as jest.Mock).mockResolvedValueOnce(ok(session));
  await expect(startNameChangeVerification()).resolves.toEqual(session);
  expect(call(1)[0]).toBe(`${BASE}/start-verification`);

  await expect(cancelNameChange()).resolves.toEqual(change);
  expect(call(2)[0]).toBe(`${BASE}/cancel`);
  expect(call(2)[1].method).toBe('POST');
});

it('a start carries the screen\'s key, the choice and the password; a free change only the password', async () => {
  const transfer = { chain: 'base', chainId: 8453, token: 'USDC', tokenAddress: '0xU', from: WALLET, to: '0xT', amount: '1.00', amountBaseUnits: '1000000', decimals: 6 };
  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ change: { ...change, status: 'paying' }, transfer }));
  await expect(startNameChange({ payMethod: 'stablecoin', chain: 'base', token: 'USDC', password: 'pw' }, 'pay-key'))
    .resolves.toEqual({ change: { ...change, status: 'paying' }, transfer });
  expect(call(0)[0]).toBe(BASE);
  expect(call(0)[1].headers['Idempotency-Key']).toBe('pay-key');
  expect(JSON.parse(String(call(0)[1].body))).toEqual({
    payMethod: 'stablecoin', chain: 'base', token: 'USDC', password: 'pw',
  });

  await startNameChange({ payMethod: 'free', password: 'pw' }, 'k2');
  expect(JSON.parse(String(call(1)[1].body))).toEqual({ payMethod: 'free', password: 'pw' });
});

it('a refusal keeps its code, blockers and next allowed date', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(refused(409, {
    success: false, code: 'money_in_flight', message: 'finish what\'s in progress before changing your name',
    data: { blockers: [{ code: 'it_buy_in_flight', message: 'An MCGP purchase is still being paid out.' }] },
  }));
  const e = await startNameChange(mcgpPay, 'k').catch((x) => x);
  expect(e).toBeInstanceOf(NameChangeError);
  expect(e.code).toBe('money_in_flight');
  expect(e.blockers).toEqual([{ code: 'it_buy_in_flight', message: 'An MCGP purchase is still being paid out.' }]);

  (global.fetch as jest.Mock).mockResolvedValueOnce(refused(409, {
    success: false, code: 'cancel_limit', message: 'you can cancel again from 2026-10-20', data: { nextAllowedAt: '2026-10-20T09:00:00Z' },
  }));
  const l = await cancelNameChange().catch((x) => x);
  expect(l.code).toBe('cancel_limit');
  expect(l.nextAllowedAt).toBe('2026-10-20T09:00:00Z');
});

it('an unproven wallet is proven through the wallet-link flow, then the start is asked once more — same key and body', async () => {
  const link = jest.spyOn(walletApi, 'linkActiveWallet').mockResolvedValueOnce({ success: true });
  (global.fetch as jest.Mock)
    .mockResolvedValueOnce(refused(403, { success: false, code: 'WALLET_PROOF_REQUIRED', message: 'Confirm you own this wallet' }))
    .mockResolvedValueOnce(ok({ change }));
  await expect(startNameChange(mcgpPay, 'pay-key')).resolves.toEqual({ change });
  expect(link).toHaveBeenCalledTimes(1);
  expect(call(1)[1].headers['Idempotency-Key']).toBe('pay-key');
  expect(call(1)[1].body).toBe(call(0)[1].body);

  // A wallet that can't be proven (no key on this phone): the linking error, and nothing asked again.
  link.mockResolvedValueOnce({ success: false, code: 'WALLET_KEY_UNAVAILABLE', message: 'This wallet\'s key isn\'t on this device' });
  (global.fetch as jest.Mock).mockResolvedValueOnce(refused(403, { success: false, code: 'WALLET_PROOF_REQUIRED', message: 'x' }));
  const e = await startNameChange(mcgpPay, 'k2').catch((x) => x);
  expect(e.code).toBe('WALLET_KEY_UNAVAILABLE');
  expect((global.fetch as jest.Mock).mock.calls).toHaveLength(3);
  link.mockRestore();
});

it('she may cancel only before her first attempt — never an MCGP debit still settling', () => {
  expect(canCancelNameChange({ status: 'paid', attempts: 0, payMethod: 'mcgp_spendable' })).toBe(true);
  expect(canCancelNameChange({ status: 'paying', attempts: 0, payMethod: 'stablecoin' })).toBe(true);
  expect(canCancelNameChange({ status: 'paying', attempts: 0, payMethod: 'mcgp_spendable' })).toBe(false);
  expect(canCancelNameChange({ status: 'declined', attempts: 1, payMethod: 'stablecoin' })).toBe(false);
  expect(canCancelNameChange({ status: 'verifying', attempts: 1, payMethod: 'mcgp_spendable' })).toBe(false);
});

it('a fee string is parsed explicitly, never coerced', () => {
  expect(parseFeeUsd('1.00')).toBe(1);
  expect(parseFeeUsd('0')).toBe(0);
  expect(parseFeeUsd(' 2.5 ')).toBe(2.5);
  for (const bad of ['', null, undefined, '1e3', '-1', '$1', '1.', '0x10']) expect(parseFeeUsd(bad)).toBeNaN();
});
