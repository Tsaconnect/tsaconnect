import {
  buildITActivity,
  fiatSymbol,
  intentActivity,
  paycrestActivity,
  relativeTime,
} from '@/utils/itActivity';
import type { ITIntent } from '@/services/itApi';
import type { PaycrestOrder } from '@/services/paycrestApi';

const symbolFor = (code: string) => (code === 'NGN' ? '₦' : code);

const order = (over: Partial<PaycrestOrder> = {}): PaycrestOrder =>
  ({
    id: 'o1',
    direction: 'sell',
    status: 'settled',
    reference: 'PCS-1',
    currency: 'NGN',
    amount: '800',
    token: 'USDC',
    chain: 'arbitrum',
    tokenAmount: '0.588214',
    walletAddress: '0xabc',
    createdAt: '2026-09-14T00:33:24Z',
    ...over,
  }) as PaycrestOrder;

const intent = (over: Partial<ITIntent> = {}): ITIntent =>
  ({
    id: 'i1',
    direction: 'buy',
    status: 'pending_payment',
    mcgpAmount: '0',
    ngnKobo: '80000',
    rateKobo: '0',
    createdAt: '2026-09-13T00:00:00Z',
    ...over,
  }) as ITIntent;

describe('paycrestActivity', () => {
  it('shows the asset sent on a sell and a short status', () => {
    const row = paycrestActivity(order(), symbolFor);
    expect(row).toMatchObject({
      direction: 'sell',
      asset: 'USDC',
      amount: '0.588214 USDC',
      status: { label: 'Complete', tone: 'success' },
      href: '/wallet/paycrest/order/o1',
    });
  });

  it('shows the fiat paid on a buy', () => {
    const row = paycrestActivity(order({ direction: 'buy', amount: '1000' }), symbolFor);
    expect(row.amount).toBe('₦1,000');
  });

  // The overflow this module exists to fix: "Expired — no payment received"
  // and "Waiting for your transfer" did not fit beside the amount.
  it('keeps every status label short enough for a pill', () => {
    const statuses = [
      'awaiting_payment',
      'awaiting_deposit',
      'pending',
      'settling',
      'settled',
      'refunded',
      'expired',
      'unmatched',
      'failed',
      'cancelled',
    ] as const;
    for (const status of statuses) {
      for (const direction of ['buy', 'sell'] as const) {
        const { label } = paycrestActivity(order({ status, direction }), symbolFor).status;
        expect(label.length).toBeLessThanOrEqual(17);
      }
    }
  });

  it('never calls an unmatched deposit a failure', () => {
    expect(paycrestActivity(order({ status: 'unmatched' }), symbolFor).status).toEqual({
      label: 'Recovering',
      tone: 'warning',
    });
  });

  it('writes the code when a currency has no symbol', () => {
    const row = paycrestActivity(order({ direction: 'buy', currency: 'UGX', amount: '5000' }), symbolFor);
    expect(row.amount).toBe('UGX 5,000');
  });
});

describe('intentActivity', () => {
  it('formats an MCGP sell from wei', () => {
    const row = intentActivity(
      intent({ direction: 'sell', status: 'credited', mcgpAmount: '9266200000000000000' }),
      symbolFor,
    );
    expect(row).toMatchObject({ asset: 'MCGP', amount: '9.2662 MCGP', status: { label: 'Complete' } });
  });

  // A stablecoin intent leaves mcgpAmount empty; it once rendered "Sell · 0 MCGP".
  it('formats a stablecoin sell from its own asset fields', () => {
    const row = intentActivity(
      intent({ direction: 'sell', asset: 'USDT', assetAmount: '12.5' }),
      symbolFor,
    );
    expect(row).toMatchObject({ asset: 'USDT', amount: '12.5 USDT' });
  });

  it('falls back to a readable label for an unknown status', () => {
    expect(intentActivity(intent({ status: 'on_hold' }), symbolFor).status).toEqual({
      label: 'On hold',
      tone: 'neutral',
    });
  });
});

describe('buildITActivity', () => {
  const intents = [
    intent({ id: 'old', createdAt: '2026-09-01T00:00:00Z' }),
    intent({ id: 'gone', status: 'cancelled', createdAt: '2026-09-15T00:00:00Z' }),
    intent({ id: 'undated', createdAt: 'not a date' }),
  ];
  const orders = [order({ id: 'new', createdAt: '2026-09-14T00:00:00Z' })];

  it('merges both systems newest first, undated last', () => {
    expect(buildITActivity(intents, orders, symbolFor).map((r) => r.key)).toEqual([
      'it-gone',
      'pc-new',
      'it-old',
      'it-undated',
    ]);
  });

  it('can hide cancelled intents for the short list', () => {
    const keys = buildITActivity(intents, orders, symbolFor, { hideCancelledIntents: true }).map(
      (r) => r.key,
    );
    expect(keys).not.toContain('it-gone');
    expect(keys).toContain('pc-new');
  });
});

describe('relativeTime', () => {
  const now = new Date('2026-09-14T12:00:00Z');
  it.each([
    ['2026-09-14T11:59:40Z', 'Just now'],
    ['2026-09-14T11:15:00Z', '45m ago'],
    ['2026-09-14T02:00:00Z', '10h ago'],
    ['2026-09-11T12:00:00Z', '3d ago'],
    ['bad', ''],
  ])('%s -> %s', (iso, want) => {
    expect(relativeTime(iso, now)).toBe(want);
  });
});

describe('fiatSymbol', () => {
  // The currency list comes from the rates API. Before it loads, or when it
  // fails, NGN must still read as ₦ — the hub hard-coded it before.
  it('falls back to a known symbol when the loaded list lacks the currency', () => {
    expect(fiatSymbol('NGN', [])).toBe('₦');
    expect(fiatSymbol('ngn', [{ code: 'USD', symbol: '$' }])).toBe('₦');
  });

  it('prefers the loaded symbol', () => {
    expect(fiatSymbol('NGN', [{ code: 'NGN', symbol: 'N' }])).toBe('N');
  });

  it('ignores a loaded "symbol" that is only the code', () => {
    expect(fiatSymbol('NGN', [{ code: 'NGN', symbol: 'NGN' }])).toBe('₦');
  });

  it('returns the code when nothing better is known', () => {
    expect(fiatSymbol('UGX', [])).toBe('UGX');
  });
});
