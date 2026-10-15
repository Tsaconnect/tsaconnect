/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The shared-pool LP screen: Activate for anyone not yet an LP; for an LP,
 * withdraw and claim are never disabled by a pause (the custody promise);
 * the claim is gas-free when it can be and signed when it cannot.
 */
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), back: jest.fn(), replace: jest.fn(), canGoBack: () => true },
    useFocusEffect: (cb: () => unknown) => react.useEffect(() => { cb(); }, [cb]),
  };
});
const mockToast = { success: jest.fn(), error: jest.fn(), info: jest.fn(), show: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  const any = (props: any) =>
    react.createElement(RNText, null, typeof props.children === 'function' ? null : props.children);
  return new Proxy({ __esModule: true, useToast: () => mockToast }, { get: (t: any, k) => (k in t ? t[k] : any) });
});
jest.mock('@/constants/chains', () => ({ __esModule: true, getChainByChainId: () => ({ nativeCurrency: { symbol: 'BNB' } }) }));
jest.mock('@/services/liquidityApi', () => ({
  __esModule: true,
  liquidityApi: { me: jest.fn(), activity: jest.fn(), activate: jest.fn(), claimFor: jest.fn() },
}));
jest.mock('@/services/liquidityTx', () => ({ __esModule: true, runLiquidityTx: jest.fn() }));
jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getSignableWallet: jest.fn(),
  // The Add sheet shows the wallet's own balance.
  readErc20BalanceOf: jest.fn(async () => 0n),
}));
jest.mock('@/services/p2p', () => ({
  __esModule: true,
  parseTokenAmount: (v: string, d: number) => {
    const [i, f = ''] = v.split('.');
    return (BigInt(i || '0') * 10n ** BigInt(d) + BigInt((f + '0'.repeat(d)).slice(0, d) || '0')).toString();
  },
  formatTokenAmount: (v: string, d: number) => {
    const p = v.padStart(d + 1, '0');
    const frac = p.slice(p.length - d).replace(/0+$/, '');
    return `${p.slice(0, p.length - d)}${frac ? `.${frac}` : ''}`;
  },
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import { liquidityApi, type LiquidityMe } from '@/services/liquidityApi';
import { runLiquidityTx } from '@/services/liquidityTx';
import { getSignableWallet } from '@/services/wallet';
import LiquidityScreen from '../index';

const api = liquidityApi as unknown as Record<'me' | 'activity' | 'activate' | 'claimFor', jest.Mock>;
const WALLET = '0x00000000000000000000000000000000000000d1';
const base: LiquidityMe = {
  mode: 'vault', status: 'none', poolOpen: true, balanceReadable: false, paused: false, allowed: false, walletChanged: false,
  chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' },
  token: { symbol: 'USDT', address: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 },
  vault: '0x00000000000000000000000000000000000000f1',
  limits: { min: '5', max: '5000' }, minClaim: '0.5',
  pool: { totalLiquidity: '12000', apr30d: '9.4' },
};
const lp: LiquidityMe = {
  ...base, status: 'active', allowed: true, balanceReadable: true, wallet: WALLET,
  principal: '100', claimable: '2', earned: '3', sharePercent: '0.83',
};

const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });
const button = (r: ReactTestRenderer, title: string) => r.root.findAll((n) => n.props.title === title)[0];
function textOf(r: ReactTestRenderer): string {
  const out: string[] = [];
  const walk = (n: unknown): void => {
    if (typeof n === 'string') out.push(n);
    else if (Array.isArray(n)) n.forEach(walk);
    else if (n && typeof n === 'object' && 'children' in n) walk((n as { children: unknown }).children);
  };
  walk(r.toJSON());
  return out.join(' ');
}
async function mount(me: LiquidityMe): Promise<ReactTestRenderer> {
  api.me.mockResolvedValue({ success: true, data: me });
  let r!: ReactTestRenderer;
  await act(async () => { r = TestRenderer.create(<LiquidityScreen />); });
  await flush();
  return r;
}

let alertSpy: jest.SpyInstance;
const lastAlert = () => alertSpy.mock.calls[alertSpy.mock.calls.length - 1];

beforeEach(() => {
  jest.clearAllMocks();
  alertSpy = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  api.activity.mockResolvedValue({ success: true, data: { items: [] } });
  (getSignableWallet as jest.Mock).mockResolvedValue(WALLET);
});

it('offers Activate to a non-LP and shows the position once activated', async () => {
  const r = await mount(base);
  expect(textOf(r)).not.toMatch(/collateral|backed/i);
  expect(r.root.findAll((n) => n.props.label === 'Total liquidity')[0].props.value).toBe('12,000 USDT');
  api.activate.mockResolvedValue({ success: true, data: lp });
  await act(async () => { button(r, 'Activate').props.onPress(); });
  await flush();
  expect(api.activate).toHaveBeenCalledTimes(1);
  expect(r.root.findAll((n) => n.props.label === 'Your liquidity')).toHaveLength(1);
});

it('keeps withdraw and claim open while paused; only Add closes', async () => {
  const r = await mount({ ...lp, status: 'paused', paused: true });
  expect(button(r, 'Add').props.disabled).toBe(true);
  expect(button(r, 'Withdraw').props.disabled).toBe(false);
  expect(button(r, 'Claim earnings').props.disabled).toBe(false);
});

it('claims gas-free above the minimum, then offers the bank cash-out', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: true, data: { txHash: '0xc' } });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(api.claimFor).toHaveBeenCalledTimes(1);
  expect(runLiquidityTx).not.toHaveBeenCalled();
  button(r, 'Cash out to your bank').props.onPress();
  expect(router.push).toHaveBeenCalledWith('/wallet/paycrest/sell?asset=USDT');
});

it('signs the claim itself below the gas-free minimum', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xc' });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(api.claimFor).not.toHaveBeenCalled();
  expect(runLiquidityTx).toHaveBeenCalledWith(expect.objectContaining({ kind: 'claim', wallet: WALLET }));
});

it('asks before a signed claim when the gas-free one is rate-limited', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: false, status: 429, message: 'later' });
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xc' });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(runLiquidityTx).not.toHaveBeenCalled();
  const [, message, buttons] = lastAlert();
  expect(message).toBe('The gas-free claim was used in the last 8 hours. Claim now with a little BNB for gas?');
  expect(buttons.map((b: { text: string }) => b.text)).toEqual(['Cancel', 'Continue']);
  await act(async () => { buttons[1].onPress(); });
  await flush();
  expect(runLiquidityTx).toHaveBeenCalledWith(expect.objectContaining({ kind: 'claim' }));
});

it('keeps Claim off after a gas-free claim until a refresh shows the claimable changed', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: true, data: { txHash: '0xc' } });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(button(r, 'Claim earnings').props.disabled).toBe(true);
  api.me.mockResolvedValue({ success: true, data: { ...lp, claimable: '0.01' } });
  const scroll = r.root.findAll((n) => typeof n.props.refreshControl === 'object' && n.props.refreshControl)[0];
  await act(async () => { await scroll.props.refreshControl.props.onRefresh(); });
  await flush();
  expect(button(r, 'Claim earnings').props.disabled).toBe(false);
});

it('explains that the gas-free minimum is the only minimum', async () => {
  const r = await mount(lp);
  expect(textOf(r)).toContain('Gas-free from 0.5 USDT; smaller claims need a little BNB.');
});

it('does not offer Activate for a new wallet while the old one still holds a position', async () => {
  const r = await mount({ ...lp, walletChanged: true });
  expect(button(r, 'Activate new wallet')).toBeUndefined();
  expect(textOf(r)).toContain(`Withdraw and claim from ${WALLET} first (switch to it on this device), then activate your new wallet.`);
});

it('asks nothing of the LP when only a pending withdrawal is left on the old wallet', async () => {
  const r = await mount({ ...lp, walletChanged: true, principal: '0', claimable: '0', pendingWithdrawal: '20' });
  expect(button(r, 'Activate new wallet')).toBeUndefined();
  expect(textOf(r)).toContain('A withdrawal from your old wallet is still pending. You can switch wallets once it completes.');
  expect(textOf(r)).not.toMatch(/first \(switch|refill/i);
});

it('offers Activate for a new wallet once the old one is empty', async () => {
  const r = await mount({ ...lp, walletChanged: true, principal: '0', claimable: '0' });
  expect(button(r, 'Activate new wallet')).toBeDefined();
});

it('explains an old-wallet refusal from the API', async () => {
  const r = await mount({ ...lp, walletChanged: true, principal: '0', claimable: '0' });
  api.activate.mockResolvedValue({ success: false, code: 'liquidity_old_wallet_has_funds', message: 'liquidity: old wallet has funds' });
  await act(async () => { button(r, 'Activate new wallet').props.onPress(); });
  await flush();
  expect(lastAlert()[1]).toBe(`Withdraw and claim from ${WALLET} first (switch to it on this device), then activate your new wallet.`);
});

it('sends an unverified user to KYC when Activate is refused for it', async () => {
  const r = await mount(base);
  api.activate.mockResolvedValue({ success: false, code: 'kyc_verification_required', message: 'liquidity: verify first' });
  await act(async () => { button(r, 'Activate').props.onPress(); });
  await flush();
  expect(lastAlert()[0]).toBe('Verify your identity');
});

it('strips the package prefix from an activate refusal', async () => {
  const r = await mount(base);
  api.activate.mockResolvedValue({ success: false, message: 'liquidity: the pool is closed' });
  await act(async () => { button(r, 'Activate').props.onPress(); });
  await flush();
  expect(lastAlert()).toEqual(["Couldn't activate", 'The pool is closed']);
});

it('tells the LP to tap Add again when the approval is still confirming', async () => {
  const r = await mount(lp);
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'pending', txHash: '0xa', step: 'approve' });
  await act(async () => { button(r, 'Add').props.onPress(); });
  await act(async () => { r.root.findAll((n) => typeof n.props.onChangeText === 'function')[0].props.onChangeText('10'); });
  const sheet = r.root.findAll((n) => n.props.footer != null)[0];
  await act(async () => { sheet.props.footer.props.onPress(); });
  await flush();
  expect(lastAlert()[1]).toBe('Approval is confirming. Tap Add again in a minute to finish adding liquidity.');
});

it('starts one transaction for a double tap', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  let release!: (w: string) => void;
  (getSignableWallet as jest.Mock).mockReturnValue(new Promise((res) => { release = res; }));
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xc' });
  const onPress = button(r, 'Claim earnings').props.onPress;
  await act(async () => { onPress(); onPress(); });
  expect(button(r, 'Claim earnings').props.disabled).toBe(true);
  await act(async () => { release(WALLET); });
  await flush();
  expect(runLiquidityTx).toHaveBeenCalledTimes(1);
});

it('signs nothing from a device that does not hold the LP wallet', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  (getSignableWallet as jest.Mock).mockResolvedValue('0x00000000000000000000000000000000000000e2');
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(runLiquidityTx).not.toHaveBeenCalled();
});

it('shows the pool card without the fields the API leaves out', async () => {
  const r = await mount({ ...base, pool: { totalLiquidity: '12000' } });
  expect(r.root.findAll((n) => n.props.label === 'Total liquidity')).toHaveLength(1);
  expect(r.root.findAll((n) => n.props.label === 'Earnings rate (last 30 days)')).toHaveLength(0);
});

it('shows a pending withdrawal without a reason', async () => {
  const r = await mount({ ...lp, pendingWithdrawal: '40' });
  expect(r.root.findAll((n) => n.props.label === 'Pending withdrawal')[0].props.value).toBe('40 USDT');
  expect(textOf(r)).not.toMatch(/refill|short/i);
  const none = await mount(lp);
  expect(none.root.findAll((n) => n.props.label === 'Pending withdrawal')).toHaveLength(0);
});

async function withdraw(r: ReactTestRenderer, value: string) {
  await act(async () => { button(r, 'Withdraw').props.onPress(); });
  await act(async () => { r.root.findAll((n) => typeof n.props.onChangeText === 'function')[0].props.onChangeText(value); });
  const sheet = r.root.findAll((n) => n.props.footer != null)[0];
  await act(async () => { sheet.props.footer.props.onPress(); });
  await flush();
}

const E18 = 10n ** 18n;

it('says how much of a withdrawal was sent now and how much is pending, from the receipt', async () => {
  const r = await mount(lp);
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xw', withdrawal: { paidNow: 30n * E18, pending: 20n * E18 } });
  // A lagging re-read must not change what the toast says.
  api.me.mockResolvedValue({ success: true, data: { ...lp, principal: '50' } });
  await withdraw(r, '50');
  expect(mockToast.success).toHaveBeenCalledWith('Withdrawal submitted. 30 USDT sent now, 20 USDT pending.');
});

it('says only the pending part when nothing was paid now', async () => {
  const r = await mount(lp);
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xw', withdrawal: { paidNow: 0n, pending: 25n * E18 / 2n } });
  await withdraw(r, '12.5');
  expect(mockToast.success).toHaveBeenCalledWith('Withdrawal submitted. 12.5 USDT pending.');
});

it('keeps the plain success when a withdrawal was paid in full', async () => {
  const r = await mount(lp);
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xw', withdrawal: { paidNow: 50n * E18, pending: 0n } });
  await withdraw(r, '50');
  expect(mockToast.success).toHaveBeenCalledWith('Withdrawal sent to your wallet');
});

it('stays neutral when the receipt could not be read', async () => {
  const r = await mount(lp);
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xw' });
  api.me.mockResolvedValue({ success: false, message: 'offline' });
  await withdraw(r, '50');
  expect(mockToast.success).toHaveBeenCalledWith('Withdrawal submitted.');
});

it('explains a gas-free claim refused for liquidity neutrally', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: false, status: 409, code: 'claim_unavailable', message: 'liquidity: insufficient liquidity' });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(lastAlert()).toEqual(["Couldn't claim", 'Claims are not available right now. Try again later.']);
});

it('explains a signed claim refused for liquidity neutrally', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'refused', code: 'claim_unavailable', message: 'liquidity: insufficient liquidity' });
  await act(async () => { button(r, 'Claim earnings').props.onPress(); });
  await flush();
  expect(lastAlert()[1]).toBe('Claims are not available right now. Try again later.');
});

it('labels pending and paid withdrawals in the activity', async () => {
  api.activity.mockResolvedValue({ success: true, data: { items: [
    { kind: 'withdrawal_pending', amount: '20', txHash: '0x1', at: '2026-10-14T10:00:00Z' },
    { kind: 'withdrawal_paid', amount: '20', txHash: '0x2', at: '2026-10-14T11:00:00Z' },
  ] } });
  const r = await mount(lp);
  const rows = r.root.findAll((n) => typeof n.props.label === 'string' && n.props.label.startsWith('Withdrawal '));
  expect(rows.map((n) => n.props.label.split(' · ')[0])).toEqual(['Withdrawal pending', 'Withdrawal paid']);
  expect(rows.map((n) => n.props.value)).toEqual(['−20 USDT', '20 USDT']);
});
