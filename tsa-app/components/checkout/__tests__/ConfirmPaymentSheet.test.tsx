/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The confirm button is the last gate before the wallet signs. It must block a
 * payment the wallet provably cannot cover, and must NOT block one whose
 * balance simply failed to load — the chain decides then.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ currency: { code: 'NGN' }, formatPrice: (usd: number) => `₦${usd}` }),
}));

jest.mock('@/components/ui', () => {
  const buttons: any[] = [];
  const Pass = ({ children }: any) => children ?? null;
  return {
    __esModule: true,
    __buttons: buttons,
    Sheet: ({ children, footer }: any) => [children, footer],
    Button: (props: any) => { buttons.push(props); return null; },
    Card: Pass,
    Divider: () => null,
    Icon: () => null,
    SummaryRow: () => null,
    Text: () => null,
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { ConfirmPaymentSheet } from '../ConfirmPaymentSheet';
import { activeStepIndex } from '../PaymentProgress';

const buttons = (jest.requireMock('@/components/ui') as any).__buttons as any[];

// 0.16 USDC total, one order.
const quote = {
  token: 'USDC',
  decimals: 6,
  usdPerToken: 1,
  lines: [
    {
      productId: 'p1', name: 'Microphone cord', sellerId: 's1', sellerName: 'Murcee', quantity: 1, kind: 'product',
      shippingZone: 'same_state', productAmount: '60000', shippingAmount: '0', platformFee: '0', gasFee: '100000',
      total: '160000',
    },
  ],
  totals: { productAmount: '60000', shippingAmount: '0', platformFee: '0', gasFee: '100000', total: '160000' },
};

const confirmButton = (tokenBalance: number | null, gasBalance: number | null) => {
  buttons.length = 0;
  let r!: ReactTestRenderer;
  act(() => {
    r = TestRenderer.create(
      <ConfirmPaymentSheet
        visible
        onClose={() => {}}
        onConfirm={() => {}}
        quote={quote}
        shippingLabel="Shipping"
        walletAddress="0x7a3f000000000000000000000000000000c91e"
        tokenBalance={tokenBalance}
        gasBalance={gasBalance}
      />,
    );
  });
  act(() => r.unmount());
  return buttons.find((b) => String(b.title).startsWith('Confirm & pay'));
};

it('shows the exact token total on the button', () => {
  expect(confirmButton(12.4, 1).title).toBe('Confirm & pay 0.16 USDC');
});

it('blocks when the pay-token balance is known to be short', () => {
  expect(confirmButton(0.1, 1).disabled).toBe(true);
});

it('blocks when the wallet is known to hold no S for gas', () => {
  expect(confirmButton(12.4, 0).disabled).toBe(true);
});

it('does not block when balances could not be read', () => {
  expect(confirmButton(null, null).disabled).toBe(false);
});

describe('activeStepIndex', () => {
  // Steps: 0 create, then per order: approve, lock.
  it('walks create → approve/lock per order → past the end when done', () => {
    expect(activeStepIndex('creating_orders', 0, 2)).toBe(0);
    expect(activeStepIndex('approving_token', 0, 2)).toBe(1);
    expect(activeStepIndex('submitting', 0, 2)).toBe(2);
    expect(activeStepIndex('approving_token', 1, 2)).toBe(3);
    expect(activeStepIndex('creating_escrow', 1, 2)).toBe(4);
    expect(activeStepIndex('done', 1, 2)).toBe(5);
  });
});

describe('Spendable MCGP', () => {
  // 40 MCGP, one order; MCGP carries no gas fee.
  const total = '40000000000000000000';
  const mcgpQuote = {
    ...quote,
    token: 'MCGP',
    decimals: 18,
    usdPerToken: 0.25,
    lines: [{ ...quote.lines[0], productAmount: total, gasFee: '0', total }],
    totals: { productAmount: total, shippingAmount: '0', platformFee: '0', gasFee: '0', total },
  };
  const mcgpButton = (tokenBalance: number | null, gasBalance: number | null) => {
    buttons.length = 0;
    let r!: ReactTestRenderer;
    act(() => {
      r = TestRenderer.create(
        <ConfirmPaymentSheet
          visible
          onClose={() => {}}
          onConfirm={() => {}}
          quote={mcgpQuote}
          shippingLabel="Shipping"
          walletAddress="0x7a3f000000000000000000000000000000c91e"
          tokenBalance={tokenBalance}
          gasBalance={gasBalance}
        />,
      );
    });
    act(() => r.unmount());
    return buttons.find((b) => String(b.title).startsWith('Confirm & pay'));
  };

  it('needs no S for gas — the platform sends the payment', () => {
    expect(mcgpButton(100, 0).disabled).toBe(false);
  });

  it('blocks when Spendable MCGP is known to be short', () => {
    expect(mcgpButton(10, 1).disabled).toBe(true);
  });

  it('counts one hold step per order instead of approve + lock', () => {
    expect(activeStepIndex('paying_spendable', 0, 2, 1)).toBe(1);
    expect(activeStepIndex('paying_spendable', 1, 2, 1)).toBe(2);
    expect(activeStepIndex('done', 1, 2, 1)).toBe(3);
  });
});
