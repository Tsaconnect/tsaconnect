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
  selectVenue,
  requestedAssetDecimals,
  formatPromisedAmount,
  formatEstimate,
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

describe('getQuote — asking for a specific (chain id, address)', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('routes to the converted-quote endpoint with chain id/address/token when the caller names a destination', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ venues: [], best: null }));

    await getQuote('buy', '150000', 'NGN', {
      token: 'USDT',
      chainId: 10,
      address: '0x94b008aa00579c1307b0ef2c499ad98a8ce58e58',
      chain: 'optimism',
    });

    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toContain('/paycrest/converted-quote?');
    expect(url).toContain('requestedChainId=10');
    expect(url).toContain('requestedTokenAddress=0x94b008aa00579c1307b0ef2c499ad98a8ce58e58');
    expect(url).toContain('requestedToken=USDT');
    expect(url).toContain('requestedChain=optimism');
  });

  it('stays on the public direct-only quote when no destination is given', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ venues: [], best: null }));

    await getQuote('buy', '150000', 'NGN');

    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toContain('/paycrest/quote?');
    expect(url).not.toContain('requestedChainId');
    expect(url).not.toContain('requestedTokenAddress');
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

  it('sends requestedChainId/requestedTokenAddress — the mechanism the server actually keys the requested asset by — and never sends decimals', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ id: 'o1', status: 'awaiting_payment' }));

    await createBuy({
      ...baseInput,
      requestedChain: 'optimism',
      requestedToken: 'USDT',
      requestedChainId: 10,
      requestedTokenAddress: '0x94b008aa00579c1307b0ef2c499ad98a8ce58e58',
    });

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    const body = JSON.parse(init.body);
    expect(body.requestedChainId).toBe(10);
    expect(body.requestedTokenAddress).toBe('0x94b008aa00579c1307b0ef2c499ad98a8ce58e58');
    // The server derives decimals itself — never trust a client-side guess.
    expect(body.requestedTokenDecimals).toBeUndefined();
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

const OPTIMISM_USDT_ADDRESS = '0x94b008aa00579c1307b0ef2c499ad98a8ce58e58';
const BASE_USDC_ADDRESS = '0x833589fcd6edb6e08f4c7c32d4f71b54bda02913';
const BASE_USDT_ADDRESS = '0xfde4c96c8593536e31f229ea8f37b2ada2699bb2';
const ARBITRUM_DAI_ADDRESS = '0xda10009cbd5d07dd0cecc66161fc93d7c9000da4';

const convertedVenue: PaycrestVenue = {
  kind: 'converted',
  chain: 'base',
  token: 'USDC',
  viaChain: 'base',
  viaToken: 'USDC',
  requestedChain: 'optimism',
  requestedToken: 'USDT',
  requestedChainId: 10,
  requestedTokenAddress: OPTIMISM_USDT_ADDRESS,
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

// A third, unrelated direct venue — present in every "no match" case below to
// prove selectVenue doesn't fall back to SOMETHING just because the list is
// non-empty.
const otherDirectVenue: PaycrestVenue = {
  kind: 'direct',
  chain: 'polygon',
  token: 'USDT',
  rate: '1640.00',
  spreadPercent: 0.3,
  tokenAmount: 91.5,
};

describe('selectVenue — the one rule this phase exists to enforce', () => {
  const wantBaseUsdc = { chain: 'base', token: 'USDC', chainId: 8453, address: BASE_USDC_ADDRESS };
  const wantOptimismUsdt = { chain: 'optimism', token: 'USDT', chainId: 10, address: OPTIMISM_USDT_ADDRESS };

  it('picks the exact direct match for what was asked', () => {
    expect(selectVenue([directVenue, otherDirectVenue], wantBaseUsdc)).toBe(directVenue);
  });

  it('is case-insensitive on chain and token', () => {
    expect(
      selectVenue([directVenue], { ...wantBaseUsdc, chain: 'BASE', token: 'usdc' }),
    ).toBe(directVenue);
  });

  it('uses the converted match only when no direct match exists for that pair', () => {
    expect(selectVenue([otherDirectVenue, convertedVenue], wantOptimismUsdt)).toBe(convertedVenue);
  });

  it('a direct match for the SAME pair always wins over a converted one', () => {
    // Mirrors the backend's Decision 3: never convert what Paycrest can
    // already deliver directly.
    const directOptimismUsdt: PaycrestVenue = {
      kind: 'direct',
      chain: 'optimism',
      token: 'USDT',
      rate: '1645.00',
      spreadPercent: 0.4,
      tokenAmount: 91.2,
    };
    const conflictingConverted: PaycrestVenue = { ...convertedVenue, chain: 'optimism', token: 'USDT' };
    expect(selectVenue([conflictingConverted, directOptimismUsdt], wantOptimismUsdt)).toBe(
      directOptimismUsdt,
    );
  });

  it('returns null — never venues[0], never "best" — when nothing matches at all', () => {
    expect(
      selectVenue([otherDirectVenue, convertedVenue], {
        chain: 'arbitrum',
        token: 'DAI',
        chainId: 42161,
        address: ARBITRUM_DAI_ADDRESS,
      }),
    ).toBeNull();
  });

  it('returns null on an empty venue list', () => {
    expect(selectVenue([], wantBaseUsdc)).toBeNull();
  });

  it('never matches a converted venue whose requestedChainId/requestedTokenAddress is a different pair', () => {
    // convertedVenue converts TO optimism/USDT — asking for base/USDT (its
    // OWN settlement chain, different token) must not match it.
    expect(
      selectVenue([convertedVenue], { chain: 'base', token: 'USDT', chainId: 8453, address: BASE_USDT_ADDRESS }),
    ).toBeNull();
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

describe('buying by address', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  const bnb = { token: 'BNB', chainId: 56, address: '0x0000000000000000000000000000000000000000' };

  it('asks the authenticated converted quote by chain id and address', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ venues: [], best: null }));
    await getQuote('buy', '20000', 'NGN', bnb);
    const url = (global.fetch as jest.Mock).mock.calls[0][0] as string;
    expect(url).toContain('/paycrest/converted-quote?');
    expect(url).toContain('requestedChainId=56');
    expect(url).toContain('requestedTokenAddress=0x0000000000000000000000000000000000000000');
    expect(url).not.toContain('requestedTokenDecimals');
  });

  it('keeps the public quote for direct-only lookups', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ venues: [], best: null }));
    await getQuote('buy', '20000', 'NGN');
    expect((global.fetch as jest.Mock).mock.calls[0][0]).toContain('/paycrest/quote?');
  });

  it('selects the converted venue by address', () => {
    const v = { chain: 'base', token: 'USDC', rate: '1500', spreadPercent: 1, tokenAmount: 1, kind: 'converted',
      requestedChainId: 56, requestedTokenAddress: bnb.address, requestedTokenDecimals: 18,
      estimatedAmount: '2200000000000000' } as PaycrestVenue;
    expect(selectVenue([v], bnb)).toBe(v);
    expect(selectVenue([v], { ...bnb, chainId: 1 })).toBeNull();
  });

  it('formats the estimate with ~', () => {
    expect(formatEstimate({ requestedTokenDecimals: 18, estimatedAmount: '2200000000000000', requestedToken: 'BNB' }))
      .toBe('~0.0022 BNB');
    expect(formatEstimate({ requestedToken: 'BNB' })).toBeNull();
  });
});
