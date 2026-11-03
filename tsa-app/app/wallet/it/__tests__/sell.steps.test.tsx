/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * The two-step IT sell: Sell (step 2) re-quotes before the PIN. That request
 * can still be in flight when the user goes back to step 1. Its answer must
 * then be dropped — applied late it would open the PIN prompt on its own the
 * next time step 2 shows, or swap the agreed sale for the one the user backed
 * away from.
 */
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
}));

jest.mock('@react-navigation/native', () => {
  const state: { enabled: boolean; onRemove: (() => void) | null } = { enabled: false, onRemove: null };
  return {
    __esModule: true,
    __state: state,
    usePreventRemove: (enabled: boolean, cb: () => void) => {
      state.enabled = enabled;
      state.onRemove = cb;
    },
  };
});

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000' }),
    spacing: { xs: 4, sm: 8, md: 12, lg: 16, xl: 24 },
    radius: { sm: 4 },
  }),
}));

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ currency: { code: 'NGN' }, formatPrice: String, allRates: {} }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => label(props.children),
    Button: function Button(props: any) {
      return label(`[${props.title}${props.disabled ? ':disabled' : ''}]`);
    },
    AmountInput: function AmountInput(props: any) {
      return label(props.label);
    },
    Spinner: (props: any) => label(props.label ?? 'spinner'),
    Icon: () => null,
    Card: box,
    Divider: () => null,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    SelectRow: (props: any) => label(`${props.label}: ${props.value}`),
    SegmentedControl: () => null,
    PickerSheet: () => null,
  };
});

jest.mock('@/components/common/AuthorizationModal', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) => (props.visible ? react.createElement(Text, null, 'PIN-PROMPT') : null),
  };
});

jest.mock('@/components/it/BankDestination', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    BankDestination: (props: any) => {
      react.useEffect(() => {
        props.onChange({
          bankCode: '044',
          bankName: 'Access',
          accountNumber: '0690000031',
          accountName: 'Ada Obi',
          ready: true,
        });
      }, []);
      return null;
    },
  };
});

// The foreign-corridor form loads bank lists over the network; these tests
// cover the NGN flow, so it only has to exist.
jest.mock('@/components/it/PayoutDetailsForm', () => ({
  __esModule: true,
  default: () => null,
  payoutDetailsComplete: (fields: { key: string }[], values: Record<string, string>) =>
    fields.every((f) => (values[f.key] ?? '').trim() !== ''),
}));

jest.mock('@/services/itApi', () => ({
  __esModule: true,
  IT_AGREEMENT_REQUIRED: 'it_agreement_required',
  IT_PRICING_UNAVAILABLE: 'it_pricing_unavailable',
  getITBalance: jest.fn(async () => ({
    success: true,
    data: { premiumBought: '0', premiumCredited: '1000000000000000000000000' },
  })),
  getWithdrawDestinations: jest.fn(async () => ({ success: false })),
  getITSellQuote: jest.fn(),
  itSell: jest.fn(),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as Nav from '@react-navigation/native';
import * as ItApi from '@/services/itApi';
import ITSell from '../sell';

const nav = (Nav as unknown as { __state: { enabled: boolean; onRemove: (() => void) | null } }).__state;
const getITSellQuote = ItApi.getITSellQuote as jest.Mock;

const fq = {
  platformFee: { inCurrency: { display: '1.00' } },
  providerFee: { fee: { display: '1.00' }, source: 'fallback' },
  tax: { total: { display: '0.00' } },
  swapFee: { fee: { display: '1.00' } },
  totalFees: { display: '3.00' },
};

function quote(mcgp: string, netKobo: string) {
  return {
    success: true,
    status: 200,
    data: {
      mode: 'mcgp',
      currency: 'NGN',
      mcgpAmount: `${mcgp}000000000000000000`,
      rateKobo: '2850',
      grossKobo: String(Number(netKobo) + 300),
      feeKobo: '300',
      netKobo,
      feeQuote: fq,
    },
  };
}

const flush = async () => {
  for (let i = 0; i < 5; i += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const text = (tree: ReactTestRenderer) => JSON.stringify(tree.toJSON());

function press(tree: ReactTestRenderer, title: string) {
  const btn = tree.root.findAll((n) => (n as any).type?.name === 'Button' && n.props.title === title)[0];
  if (!btn) throw new Error(`no ${title} button`);
  if (btn.props.disabled) throw new Error(`${title} is disabled`);
  // Fire and let it run: onSell may be parked on a request the test holds.
  return act(async () => {
    void btn.props.onPress();
  });
}

async function typeAmount(tree: ReactTestRenderer, value: string) {
  const input = tree.root.findAll((n) => (n as any).type?.name === 'AmountInput')[0];
  await act(async () => {
    input.props.onChangeText(value);
  });
  // Past the quote debounce.
  await act(async () => {
    jest.advanceTimersByTime(500);
  });
  await flush();
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

async function mountAtPayout(amount: string, q: ReturnType<typeof quote>) {
  getITSellQuote.mockResolvedValueOnce(q);
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = TestRenderer.create(React.createElement(ITSell));
  });
  await flush();
  await typeAmount(tree, amount);
  await press(tree, 'Continue');
  await flush();
  expect(text(tree)).toContain('STEP 2 OF 2');
  return tree;
}

describe('IT sell — step 2 re-quote vs. going back', () => {
  beforeEach(() => {
    jest.useFakeTimers();
    getITSellQuote.mockReset();
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  it('opens the PIN prompt when the re-quote agrees (control)', async () => {
    const tree = await mountAtPayout('100', quote('100', '284700'));
    getITSellQuote.mockResolvedValueOnce(quote('100', '284700'));
    await press(tree, 'Sell');
    await flush();
    expect(text(tree)).toContain('PIN-PROMPT');
  });

  it('drops a re-quote that lands after the user went back', async () => {
    const tree = await mountAtPayout('100', quote('100', '284700'));
    const late = deferred<ReturnType<typeof quote>>();
    getITSellQuote.mockReturnValueOnce(late.promise);
    await press(tree, 'Sell');

    // Header / Android back while the re-quote is in flight.
    await act(async () => {
      nav.onRemove?.();
    });
    expect(text(tree)).toContain('STEP 1 OF 2');

    await act(async () => {
      late.resolve(quote('100', '284700'));
    });
    await flush();

    // Back to step 2: nothing may be waiting to prompt for the PIN.
    await press(tree, 'Continue');
    await flush();
    expect(text(tree)).toContain('STEP 2 OF 2');
    expect(text(tree)).not.toContain('PIN-PROMPT');
  });

  it('never swaps the agreed sale for the one the user backed away from', async () => {
    const tree = await mountAtPayout('100', quote('100', '284700'));
    const late = deferred<ReturnType<typeof quote>>();
    getITSellQuote.mockReturnValueOnce(late.promise);
    await press(tree, 'Sell');

    // The Back button this time, then a different amount.
    await press(tree, 'Back');
    getITSellQuote.mockResolvedValueOnce(quote('50', '142200'));
    await typeAmount(tree, '50');
    await press(tree, 'Continue');
    await flush();
    expect(text(tree)).toContain('You sell: 50 MCGP');

    await act(async () => {
      late.resolve(quote('100', '284700'));
    });
    await flush();

    expect(text(tree)).toContain('You sell: 50 MCGP');
    expect(text(tree)).not.toContain('PIN-PROMPT');
  });
});

describe('IT sell — step 2 payout currency change', () => {
  beforeEach(() => {
    jest.useFakeTimers();
    getITSellQuote.mockReset();
  });
  afterEach(() => {
    jest.useRealTimers();
  });

  const pickCurrency = (tree: ReactTestRenderer, id: string) => {
    const sheet = tree.root.findAll((n) => n.props.title === 'Payout currency' && typeof n.props.onSelect === 'function')[0];
    return act(async () => {
      sheet.props.onSelect({ id });
    });
  };

  it('shows the converted amount once the new currency is priced', async () => {
    const tree = await mountAtPayout('100', quote('100', '284700'));
    const usdQuote = quote('100', '280000');
    getITSellQuote.mockResolvedValueOnce({
      ...usdQuote,
      data: { ...usdQuote.data, currency: 'USD', destinationAmount: '1.95' },
    });
    await pickCurrency(tree, 'USD');
    await flush();
    expect(text(tree)).toContain('You receive: ≈ 1.95 USD');
    expect(text(tree)).toContain('[Sell]');
  });

  it('re-prices for the new currency, holds Sell meanwhile, and drops a stale answer', async () => {
    const tree = await mountAtPayout('100', quote('100', '284700'));

    const usd = deferred<unknown>();
    getITSellQuote.mockReturnValueOnce(usd.promise);
    await pickCurrency(tree, 'USD');
    expect(getITSellQuote).toHaveBeenLastCalledWith({ mcgpAmount: '100000000000000000000' }, 'USD');
    // Until the USD price lands the summary shows no naira figures as USD.
    expect(text(tree)).toContain('You receive: …');
    expect(text(tree)).toContain('[Sell:disabled]');

    // Back to NGN before the USD answer arrives: the USD answer must not land.
    getITSellQuote.mockResolvedValueOnce(quote('100', '284700'));
    await pickCurrency(tree, 'NGN');
    await flush();
    const usdQuote = quote('100', '284700');
    await act(async () => {
      usd.resolve({ ...usdQuote, data: { ...usdQuote.data, currency: 'USD', destinationAmount: '2.00' } });
    });
    await flush();
    expect(text(tree)).not.toContain('≈');
    expect(text(tree)).not.toContain('You receive: …');
    expect(text(tree)).toContain('[Sell]');
  });
});
