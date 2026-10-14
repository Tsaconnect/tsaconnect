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
jest.mock('@/services/wallet', () => ({ __esModule: true, getSignableWallet: jest.fn() }));
jest.mock('@/services/p2p', () => ({
  __esModule: true,
  parseTokenAmount: (v: string, d: number) => {
    const [i, f = ''] = v.split('.');
    return (BigInt(i || '0') * 10n ** BigInt(d) + BigInt((f + '0'.repeat(d)).slice(0, d) || '0')).toString();
  },
  formatTokenAmount: (v: string) => v,
}));

import React from 'react';
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
  pool: { totalLiquidity: '12000', collateral: '3000', outstanding: '200', coveragePercent: '100', apr30d: '9.4' },
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

beforeEach(() => {
  jest.clearAllMocks();
  api.activity.mockResolvedValue({ success: true, data: { items: [] } });
  (getSignableWallet as jest.Mock).mockResolvedValue(WALLET);
});

it('offers Activate to a non-LP and shows the position once activated', async () => {
  const r = await mount(base);
  expect(textOf(r)).toContain('100%');
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
  expect(button(r, 'Claim earnings (min 0.5 USDT)').props.disabled).toBe(false);
});

it('claims gas-free above the minimum, then offers the bank cash-out', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: true, data: { txHash: '0xc' } });
  await act(async () => { button(r, 'Claim earnings (min 0.5 USDT)').props.onPress(); });
  await flush();
  expect(api.claimFor).toHaveBeenCalledTimes(1);
  expect(runLiquidityTx).not.toHaveBeenCalled();
  button(r, 'Cash out to your bank').props.onPress();
  expect(router.push).toHaveBeenCalledWith('/wallet/paycrest/sell?asset=USDT');
});

it('signs the claim itself below the gas-free minimum', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xc' });
  await act(async () => { button(r, 'Claim earnings (min 0.5 USDT)').props.onPress(); });
  await flush();
  expect(api.claimFor).not.toHaveBeenCalled();
  expect(runLiquidityTx).toHaveBeenCalledWith(expect.objectContaining({ kind: 'claim', wallet: WALLET }));
});

it('falls back to a signed claim when the gas-free one is rate-limited', async () => {
  const r = await mount(lp);
  api.claimFor.mockResolvedValue({ success: false, status: 429, message: 'later' });
  (runLiquidityTx as jest.Mock).mockResolvedValue({ status: 'done', txHash: '0xc' });
  await act(async () => { button(r, 'Claim earnings (min 0.5 USDT)').props.onPress(); });
  await flush();
  expect(runLiquidityTx).toHaveBeenCalledWith(expect.objectContaining({ kind: 'claim' }));
});

it('signs nothing from a device that does not hold the LP wallet', async () => {
  const r = await mount({ ...lp, claimable: '0.2' });
  (getSignableWallet as jest.Mock).mockResolvedValue('0x00000000000000000000000000000000000000e2');
  await act(async () => { button(r, 'Claim earnings (min 0.5 USDT)').props.onPress(); });
  await flush();
  expect(runLiquidityTx).not.toHaveBeenCalled();
});
