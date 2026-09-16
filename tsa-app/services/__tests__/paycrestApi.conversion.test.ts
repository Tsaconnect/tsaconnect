// services/__tests__/paycrestApi.conversion.test.ts
//
// Phase 2 "converted buys": Paycrest sometimes cannot deliver the exact
// token/chain the user asked for, but can convert into it via a treasury +
// cross-chain route. Nothing here is allowed to hide that substitution — a
// converted venue/order must always say what it settles as, what it delivers,
// and roughly how long that takes.
//
// The AsyncStorage mock is required — see paycrestApi.claim.test.ts.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import {
  createBuy,
  getQuote,
  isConvertedVenue,
  isConvertedOrder,
  requestedAssetDecimals,
  formatPromisedAmount,
  formatEta,
  describeConvertedVenue,
  paycrestConversionHops,
  statusLabel,
  type PaycrestVenue,
} from '../paycrestApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

describe('getQuote — asking for a specific (chain, token)', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('sends requestedChain/requestedToken when the caller names a destination', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ venues: [], best: null }));

    await getQuote('buy', '150000', 'NGN', { chain: 'optimism', token: 'USDT' });

    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toContain('requestedChain=optimism');
    expect(url).toContain('requestedToken=USDT');
  });

  it('omits them entirely when no destination is given, so an unwired server sees the old request', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ venues: [], best: null }));

    await getQuote('buy', '150000', 'NGN');

    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).not.toContain('requestedChain');
    expect(url).not.toContain('requestedToken');
  });
});

describe('createBuy — passing through what the user actually wants', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  const baseInput = {
    amount: '150000',
    chain: 'base',
    token: 'USDC',
    walletAddress: '0x000000000000000000000000000000000000dEaD',
    refundBankCode: '044',
    refundAccount: '0123456789',
    currency: 'NGN',
  };

  it('sends the requested chain and token when the venue converts', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ id: 'o1', status: 'awaiting_payment' }));

    await createBuy({ ...baseInput, requestedChain: 'optimism', requestedToken: 'USDT' });

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    const body = JSON.parse(init.body);
    expect(body.requestedChain).toBe('optimism');
    expect(body.requestedToken).toBe('USDT');
    // The settlement leg is unchanged — the server still needs to know what
    // Paycrest itself is being asked to fill.
    expect(body.chain).toBe('base');
    expect(body.token).toBe('USDC');
  });

  it('omits requestedChain/requestedToken on a direct buy, matching old behaviour exactly', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ id: 'o1', status: 'awaiting_payment' }));

    await createBuy(baseInput);

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    const body = JSON.parse(init.body);
    expect(body.requestedChain).toBeUndefined();
    expect(body.requestedToken).toBeUndefined();
  });
});

const convertedVenue: PaycrestVenue = {
  kind: 'converted',
  chain: 'base',
  token: 'USDC',
  viaChain: 'base',
  viaToken: 'USDC',
  requestedChain: 'optimism',
  requestedToken: 'USDT',
  rate: '1650.00',
  spreadPercent: 0.5,
  tokenAmount: 99.5,
  markupBps: 50,
  promisedAmount: '99500000', // 99.5 USDT at 6dp
  etaSeconds: 30,
};

const directVenue: PaycrestVenue = {
  kind: 'direct',
  chain: 'base',
  token: 'USDC',
  rate: '1650.00',
  spreadPercent: 0.5,
  tokenAmount: 100,
};

describe('telling a converted venue apart from a direct one', () => {
  it('is converted when kind says so', () => {
    expect(isConvertedVenue(convertedVenue)).toBe(true);
    expect(isConvertedVenue(directVenue)).toBe(false);
    // Absent kind (an older server build) must read as direct, not converted.
    expect(isConvertedVenue({})).toBe(false);
  });

  it('is converted on the order the same way', () => {
    expect(isConvertedOrder({ routeKind: 'converted' })).toBe(true);
    expect(isConvertedOrder({ routeKind: 'direct' })).toBe(false);
    expect(isConvertedOrder({})).toBe(false);
  });
});

describe('describeConvertedVenue — the no-silent-substitution sentence', () => {
  const label = (key: string) => ({ base: 'Base', optimism: 'Optimism' }[key] ?? key);

  it('states what the user receives, that it converts, and roughly how long', () => {
    const text = describeConvertedVenue(convertedVenue, label);
    expect(text).toContain('USDT on Optimism');
    expect(text).toMatch(/conversion/i);
    expect(text).toContain('USDC on Base');
    expect(text).toContain('~30s');
  });
});

describe('formatEta', () => {
  it('reads in seconds under 90s', () => {
    expect(formatEta(30)).toBe('~30s');
    expect(formatEta(89)).toBe('~89s');
  });
  it('reads in minutes at and above 90s', () => {
    expect(formatEta(90)).toBe('~2 min');
    expect(formatEta(150)).toBe('~3 min');
  });
  it('never claims an instant delivery it cannot promise', () => {
    expect(formatEta(0)).toBe('a few minutes');
    expect(formatEta(undefined)).toBe('a few minutes');
  });
});

describe('requestedAssetDecimals — the BSC 18dp trap', () => {
  it('is 6 for USDT/USDC on an ordinary chain', () => {
    expect(requestedAssetDecimals('base', 'USDT')).toBe(6);
    expect(requestedAssetDecimals('optimism', 'USDC')).toBe(6);
  });
  it('is 18 for USDT/USDC on BSC specifically', () => {
    expect(requestedAssetDecimals('bsc', 'USDT')).toBe(18);
  });
});

describe('formatPromisedAmount — base units in the REQUESTED token, never raw', () => {
  it('renders 99500000 base units of 6dp USDT as 99.5', () => {
    expect(formatPromisedAmount(convertedVenue)).toBe('99.5');
  });
  it('rescales an 18-decimal requested token correctly', () => {
    expect(
      formatPromisedAmount({
        promisedAmount: '99500000000000000000', // 99.5 at 18dp
        requestedChain: 'bsc',
        requestedToken: 'USDT',
      }),
    ).toBe('99.5');
  });
  it('is null when any field needed to price it is missing (a direct venue)', () => {
    expect(formatPromisedAmount(directVenue)).toBeNull();
  });
});

describe('statusLabel for the converted-order states', () => {
  const order = (status: any) => ({ status, direction: 'buy' } as any);
  it('has its own wording, not a raw snake_case fallthrough', () => {
    expect(statusLabel(order('converting'))).toMatch(/convert/i);
    expect(statusLabel(order('refunding'))).toMatch(/wallet/i);
    expect(statusLabel(order('recovery_pending'))).toMatch(/recover/i);
  });
});

describe('paycrestConversionHops — Received → Converting → Delivering', () => {
  const stateOf = (status: any, holder?: string) => {
    const hops = paycrestConversionHops(status, holder);
    return hops.map((h) => h.state);
  };

  it('pending/settling: Received is the active hop', () => {
    expect(stateOf('pending')).toEqual(['active', 'pending', 'pending']);
    expect(stateOf('settling')).toEqual(['active', 'pending', 'pending']);
  });

  it('converting with holder=treasury: Received done, Converting active', () => {
    expect(stateOf('converting', 'treasury')).toEqual(['done', 'active', 'pending']);
  });

  it('converting with a stale holder=paycrest: Received not claimed yet', () => {
    expect(stateOf('converting', 'paycrest')).toEqual(['active', 'active', 'pending']);
  });

  it('settled: every hop reads done', () => {
    expect(stateOf('settled')).toEqual(['done', 'done', 'done']);
  });

  it('refunding: Delivering is replaced by "Returning to your wallet", active', () => {
    const hops = paycrestConversionHops('refunding');
    expect(hops[2].label).toBe('Returning to your wallet');
    expect(hops.map((h) => h.state)).toEqual(['done', 'done', 'active']);
  });

  it('refunded: reads as returned, done — never as a failed delivery', () => {
    const hops = paycrestConversionHops('refunded');
    expect(hops[2].label).toBe('Returned to your wallet');
    expect(hops.map((h) => h.state)).toEqual(['done', 'done', 'done']);
  });

  it('recovery_pending: Converting reads failed, nothing claims delivery', () => {
    expect(stateOf('recovery_pending')).toEqual(['done', 'failed', 'pending']);
  });

  it('a dead order (expired/failed/cancelled) never claims progress it never made', () => {
    expect(stateOf('expired')).toEqual(['failed', 'pending', 'pending']);
    expect(stateOf('failed')).toEqual(['failed', 'pending', 'pending']);
    expect(stateOf('cancelled')).toEqual(['failed', 'pending', 'pending']);
  });
});
