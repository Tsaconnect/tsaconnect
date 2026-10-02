/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The "pay with another token" confirm sheet (owner 2026-10-02): what leaves
 * her wallet, what it costs in one line, and that the money is held in escrow.
 * The plumbing (the Sonic hop, wallet balance used first, the per-fee split)
 * lives behind "Details", and nothing says we cover a fee she actually pays.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null), removeItem: jest.fn(async () => null) },
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
    type: new Proxy({}, { get: () => ({ fontSize: 14, lineHeight: 20, weight: '400' }) }),
    fonts: new Proxy({}, { get: () => 'System' }),
  }),
  formatFiat: (n: number) => String(n),
  formatNumber: (n: number) => String(n),
}));

jest.mock('@/components/ui/Sheet', () => {
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Sheet: ({ visible, title, children, footer }: any) =>
      visible ? (
        <View testID="sheet" accessibilityLabel={title}>
          {children}
          {footer}
        </View>
      ) : null,
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { FundingConfirmSheet } from '@/components/checkout/PayWithToken';
import type { FundingQuote } from '@/services/checkoutFundingApi';

// The 2026-10-02 screenshot: a 0.16 USDC order, 0.02 already on Sonic, paid
// with USDC on Base.
const quote: FundingQuote = {
  groupId: 'g', orders: 1, token: 'USDC', tokenAddress: '0x2921', chainId: 146,
  orderTotal: '160000', walletBalance: '20000', shortfall: '140000', toAmount: '140700',
  fromAmount: '181227', fromAmountUsd: 0.181, lifiCostUsd: 0.02, gasUsd: 0.02,
  costBps: 110, gasBps: 110, budgetBps: 10000, etaSeconds: 20, tool: 'relay',
  sourceGasTopUp: true, destGasTopUp: false,
};
const pick = { token: { symbol: 'USDC', decimals: 6, address: '0x8335', chainId: 8453, name: 'USD Coin' } as any, chainId: 8453 };

function renderSheet(over: Partial<React.ComponentProps<typeof FundingConfirmSheet>> = {}): ReactTestRenderer {
  let tree!: ReactTestRenderer;
  act(() => {
    tree = TestRenderer.create(
      <FundingConfirmSheet visible onClose={jest.fn()} onConfirm={jest.fn()} quote={quote} pick={pick} stale={false} {...over} />,
    );
  });
  return tree;
}

/** Every rendered string, so assertions read like what she sees. */
function texts(tree: ReactTestRenderer): string[] {
  const out: string[] = [];
  const walk = (node: any) => {
    if (typeof node === 'string') out.push(node);
    else if (node && Array.isArray(node.children)) node.children.forEach(walk);
    else if (Array.isArray(node)) node.forEach(walk);
  };
  walk(tree.toJSON());
  return out;
}
const has = (tree: ReactTestRenderer, t: string | RegExp) =>
  texts(tree).some(s => (typeof t === 'string' ? s === t : t.test(s)));
const press = (tree: ReactTestRenderer, testID: string) =>
  act(() => { tree.root.findAll(n => n.props.testID === testID && typeof n.props.onPress === 'function')[0].props.onPress(); });
const pressButton = (tree: ReactTestRenderer, title: string) =>
  act(() => { tree.root.findAll(n => n.props.title === title && typeof n.props.onPress === 'function')[0].props.onPress(); });

describe('FundingConfirmSheet', () => {
  it('leads with what she pays, on which chain, and the fees in one line', () => {
    const tree = renderSheet();
    expect(has(tree, '0.181227 USDC')).toBe(true);
    expect(has(tree, '≈ $0.18 on Base')).toBe(true);
    expect(has(tree, 'Includes $0.04 in conversion and network fees')).toBe(true);
    expect(has(tree, /held in escrow/i)).toBe(true);
  });

  it('never claims we cover a fee she pays', () => {
    const tree = renderSheet();
    expect(has(tree, /we cover/i)).toBe(false);
    press(tree, 'funding-details-toggle');
    expect(has(tree, /we cover/i)).toBe(false);
  });

  it('keeps the plumbing behind Details', () => {
    const tree = renderSheet();
    expect(has(tree, 'Order total')).toBe(false);
    expect(has(tree, /Sonic/)).toBe(false);
    press(tree, 'funding-details-toggle');
    for (const label of ['Order total', 'From your USDC balance', 'Converted to USDC', 'Conversion fee', 'Network fees']) {
      expect(has(tree, label)).toBe(true);
    }
  });

  it('names the amount on the button, or asks for a fresh price', () => {
    const onConfirm = jest.fn();
    const tree = renderSheet({ onConfirm });
    expect(has(tree, 'Pay 0.181227 USDC')).toBe(true);
    pressButton(tree, 'Pay 0.181227 USDC');
    expect(onConfirm).toHaveBeenCalled();
    expect(has(renderSheet({ stale: true }), 'Refresh the price')).toBe(true);
  });

  // On device (2026-10-02) the parts read $0.02 + $0.01 under a "$0.04" total:
  // each was rounded to the cent on its own. Sub-dollar fees keep a third
  // decimal, and the total is the sum of what is shown.
  it('shows fee parts that add up to the total it states', () => {
    const tree = renderSheet({ quote: { ...quote, lifiCostUsd: 0.0244, gasUsd: 0.0144 } });
    expect(has(tree, 'Includes $0.038 in conversion and network fees')).toBe(true);
    press(tree, 'funding-details-toggle');
    expect(has(tree, '$0.024')).toBe(true);
    expect(has(tree, '$0.014')).toBe(true);
    // Whole cents stay two decimals, whatever floating point makes of them.
    const cents = renderSheet({ quote: { ...quote, lifiCostUsd: 0.07, gasUsd: 0.01 } });
    expect(has(cents, 'Includes $0.08 in conversion and network fees')).toBe(true);
  });

  it('says how many sellers a multi-seller checkout pays', () => {
    const tree = renderSheet({ quote: { ...quote, orders: 3 } });
    press(tree, 'funding-details-toggle');
    expect(has(tree, 'Sellers paid')).toBe(true);
    expect(has(tree, '3')).toBe(true);
  });
});
