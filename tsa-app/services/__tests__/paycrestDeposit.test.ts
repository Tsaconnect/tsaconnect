// services/__tests__/paycrestDeposit.test.ts
//
// The sell leg's in-app send: checkFunding decides whether a route is
// spendable BEFORE an order (and its countdown) is created, and
// sendPaycrestDeposit moves the stablecoin from the device wallet to the
// provider's deposit address.
//
// checkFunding runs for real with no mocks — it is a pure predicate over a
// venue and the wallet's balances. sendPaycrestDeposit mocks only its
// collaborators (prepare/sign/submit); sendPreparedTransaction itself is the
// real one, so the nonce retry it wraps is exercised here rather than faked.
//
// The AsyncStorage mock is required: walletApi's getAuthHeaders reads it at
// import time reach, and without the mock every call fails before fetch.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

const mockSignTransaction = jest.fn();
const mockSignTypedData = jest.fn();
jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSignTransaction(...a),
  signTypedData: (...a: any[]) => mockSignTypedData(...a),
  getActiveWallet: async () => '0xfrom',
}));

const mockPrepare = jest.fn();
const mockSubmit = jest.fn();
jest.mock('../walletApi', () => {
  const actual = jest.requireActual('../walletApi');
  return {
    ...actual,
    prepareSendTransaction: (...a: any[]) => mockPrepare(...a),
    submitTransaction: (...a: any[]) => mockSubmit(...a),
  };
});

const mockGetOffer = jest.fn();
const mockRelay = jest.fn();
const mockTopUp = jest.fn();
jest.mock('../gasSponsorship', () => {
  const actual = jest.requireActual('../gasSponsorship');
  return {
    ...actual,
    getSponsorshipOffer: (...a: any[]) => mockGetOffer(...a),
    relayAuthorization: (...a: any[]) => mockRelay(...a),
    requestGasTopUp: (...a: any[]) => mockTopUp(...a),
  };
});

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import {
  checkFunding, fmtTokenAmount, fundingMessage, nativeBalanceFor, sendPaycrestDeposit, sendSponsoredPaycrestDeposit,
} from '../paycrestDeposit';
// eslint-disable-next-line import/first
import { PaycrestError } from '../paycrestApi';
// eslint-disable-next-line import/first
import type { WalletBalanceWithChain } from '../walletApi';

const bal = (
  chainKey: string,
  symbol: string,
  balance: string,
): WalletBalanceWithChain =>
  ({
    chainKey,
    chainName: chainKey,
    symbol,
    name: symbol,
    balance,
    usdValue: '0',
    contractAddress: '',
    decimals: 6,
  }) as WalletBalanceWithChain;

const venue = { chain: 'base', token: 'USDC', tokenAmount: 36.4218 };

const prepared = {
  to: '0xusdc',
  data: '0xa9059cbb',
  value: '0',
  gasLimit: '65000',
  gasPrice: '1000000000', // 1 gwei → 0.000065 ETH
  nonce: 7,
  chainId: 8453,
};

describe('checkFunding', () => {
  it('passes a route the wallet can cover', () => {
    const result = checkFunding(venue, [bal('base', 'USDC', '50'), bal('base', 'ETH', '0.004')]);

    expect(result).toEqual({ ok: true });
  });

  it('passes when the balance exactly equals the amount to send', () => {
    const result = checkFunding(venue, [
      bal('base', 'USDC', '36.4218'),
      bal('base', 'ETH', '0.004'),
    ]);

    expect(result).toEqual({ ok: true });
  });

  it('does not block when balances could not be loaded', () => {
    // A flaky indexer must never lock a user out of a sale they can fund.
    expect(checkFunding(venue, null)).toEqual({ ok: true });
  });

  it('reports the shortfall with what the wallet actually holds', () => {
    const result = checkFunding(venue, [bal('base', 'USDC', '12.4'), bal('base', 'ETH', '0.004')]);

    expect(result).toEqual({
      ok: false,
      reason: 'short-token',
      token: 'USDC',
      chain: 'base',
      have: 12.4,
      need: 36.4218,
    });
  });

  it('treats a token the wallet has never held as a zero balance', () => {
    const result = checkFunding(venue, [bal('base', 'ETH', '0.004')]);

    expect(result).toMatchObject({ ok: false, reason: 'short-token', have: 0 });
  });

  it('ignores the same token on a different chain', () => {
    // 50 USDC on Polygon cannot pay a Base deposit address.
    const result = checkFunding(venue, [bal('polygon', 'USDC', '50'), bal('base', 'ETH', '0.004')]);

    expect(result).toMatchObject({ ok: false, reason: 'short-token', have: 0 });
  });

  it('flags an empty gas balance on an otherwise fundable route', () => {
    const result = checkFunding(venue, [bal('base', 'USDC', '50'), bal('base', 'ETH', '0')]);

    expect(result).toEqual({
      ok: false,
      reason: 'no-gas',
      nativeSymbol: 'ETH',
      chain: 'base',
    });
  });

  it('reports the token shortfall first when the wallet is short of both', () => {
    // Topping up gas would not help — the sale still cannot happen.
    const result = checkFunding(venue, [bal('base', 'USDC', '1'), bal('base', 'ETH', '0')]);

    expect(result).toMatchObject({ reason: 'short-token' });
  });

  it('rejects a chain this app cannot sign for', () => {
    const result = checkFunding({ ...venue, chain: 'celo' }, [bal('base', 'USDC', '50')]);

    expect(result).toEqual({ ok: false, reason: 'unsupported-chain', chain: 'celo' });
  });
});

describe('sendPaycrestDeposit', () => {
  const order = {
    chain: 'base',
    token: 'USDC',
    tokenAmount: '36.4218',
    depositAddress: '0xprovider',
  };

  beforeEach(() => {
    mockPrepare.mockReset();
    mockSubmit.mockReset();
    mockSignTransaction.mockReset();
    mockPrepare.mockResolvedValue({ success: true, data: prepared });
    mockSignTransaction.mockResolvedValue('0xsigned');
    mockSubmit.mockResolvedValue({ success: true, data: { txHash: '0xhash' } });
  });

  it('sends the order amount to the deposit address on the order chain', async () => {
    const hash = await sendPaycrestDeposit(order, '0xfrom');

    expect(hash).toBe('0xhash');
    expect(mockPrepare).toHaveBeenCalledWith('USDC', '0xprovider', '36.4218', 8453, {
      fromAddress: '0xfrom',
    });
  });

  it('signs and broadcasts from the same wallet the tx was prepared for', async () => {
    // The backend's submit guard rejects a signature that recovers to any
    // other address, so one captured address must reach all three steps.
    await sendPaycrestDeposit(order, '0xfrom');

    expect(mockSignTransaction).toHaveBeenCalledWith(expect.objectContaining({ nonce: 7 }), '0xfrom');
    const [signedTx, txType, token, to, amount, chainId, from] = mockSubmit.mock.calls[0];
    expect(signedTx).toBe('0xsigned');
    expect(txType).toBe('send');
    expect([token, to, amount, chainId, from]).toEqual(['USDC', '0xprovider', '36.4218', 8453, '0xfrom']);
  });

  it('refuses a chain the app cannot sign for, before any network call', async () => {
    await expect(sendPaycrestDeposit({ ...order, chain: 'celo' }, '0xfrom')).rejects.toThrow(/celo/);

    expect(mockPrepare).not.toHaveBeenCalled();
  });

  it('refuses an order with no deposit address', async () => {
    await expect(
      sendPaycrestDeposit({ ...order, depositAddress: undefined }, '0xfrom'),
    ).rejects.toThrow(/deposit address/i);

    expect(mockPrepare).not.toHaveBeenCalled();
  });

  it('maps a rejected broadcast to display copy, not the RPC\'s own words', async () => {
    mockSubmit.mockResolvedValue({ success: false, message: 'insufficient funds for transfer' });

    await expect(sendPaycrestDeposit(order, '0xfrom')).rejects.toThrow(
      'Not enough native token to pay gas on this chain.',
    );
  });

  const BASE_FEE_ERR =
    'Failed to submit transaction to blockchain on arbitrum: failed to send transaction: ' +
    'max fee per gas less than block base fee: address 0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce, ' +
    'maxFeePerGas: 20006000 baseFee: 20098000';

  // The screen shows whatever this rejects with, verbatim, in a red box.
  it('never puts a raw gas-fee rejection in front of the user', async () => {
    mockSubmit.mockResolvedValue({ success: false, message: BASE_FEE_ERR });

    await expect(sendPaycrestDeposit(order, '0xfrom')).rejects.toThrow(
      'Network fees rose while you were confirming. Please try again.',
    );
    // It reached that message via the retry, not on the first rejection.
    expect(mockSubmit).toHaveBeenCalledTimes(2);
  });

  // The whole point of the retry: a base-fee tick should cost a round trip, not
  // the sale. Exactly one transfer must leave the wallet.
  it('recovers from a stale base fee by re-pricing, sending once', async () => {
    mockSubmit
      .mockResolvedValueOnce({ success: false, message: BASE_FEE_ERR })
      .mockResolvedValueOnce({ success: true, data: { txHash: '0xsecond' } });

    const hash = await sendPaycrestDeposit(order, '0xfrom');

    expect(hash).toBe('0xsecond');
    expect(mockSubmit).toHaveBeenCalledTimes(2);
    // Both attempts moved the same amount to the same address.
    for (const call of mockSubmit.mock.calls) {
      expect(call[3]).toBe('0xprovider');
      expect(call[4]).toBe('36.4218');
    }
  });

  it('names the gas shortfall instead of signing a tx that cannot pay for itself', async () => {
    // 0.000065 ETH of gas against a 0.00001 ETH balance.
    await expect(sendPaycrestDeposit(order, '0xfrom', 0.00001)).rejects.toThrow(/ETH.*gas/i);

    expect(mockSignTransaction).not.toHaveBeenCalled();
  });

  it('sends when the gas cost fits the native balance', async () => {
    await expect(sendPaycrestDeposit(order, '0xfrom', 0.01)).resolves.toBe('0xhash');
  });

  it('sends when the native balance is unknown', async () => {
    // Balances failed to load — the same rule as checkFunding: never block.
    await expect(sendPaycrestDeposit(order, '0xfrom', null)).resolves.toBe('0xhash');
  });
});

describe('checkFunding with gas sponsorship', () => {
  it('does not report no-gas on a sponsored route', () => {
    const result = checkFunding(venue, [bal('base', 'USDC', '50')], { gasSponsored: true });
    expect(result).toEqual({ ok: true });
  });

  it('still reports a token shortfall on a sponsored route', () => {
    const result = checkFunding(venue, [bal('base', 'USDC', '1')], { gasSponsored: true });
    expect(result).toMatchObject({ ok: false, reason: 'short-token' });
  });
});

describe('sendSponsoredPaycrestDeposit', () => {
  const SIGNER = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
  const order = {
    id: 'order-1',
    chain: 'base',
    token: 'USDC',
    tokenAmount: '36.421800',
    depositAddress: '0x2222222222222222222222222222222222222222',
    walletAddress: SIGNER,
  };
  const authOffer = {
    mode: 'authorization' as const,
    chain: 'base',
    token: 'USDC',
    decimals: 6,
    from: SIGNER,
    to: order.depositAddress,
    value: '36421800',
    validBefore: 1790000000,
    domain: { name: 'USD Coin', version: '2', chainId: '8453', verifyingContract: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' },
  };

  beforeEach(() => {
    jest.clearAllMocks();
    mockPrepare.mockResolvedValue({ success: true, data: prepared });
    mockSubmit.mockResolvedValue({ success: true, data: { txHash: '0xhash' } });
    mockSignTransaction.mockResolvedValue('0xsigned');
  });

  it('signs and relays an authorization without sending from the wallet', async () => {
    mockGetOffer.mockResolvedValue(authOffer);
    mockSignTypedData.mockResolvedValue('0x' + '00'.repeat(65));
    mockRelay.mockResolvedValue({ txHash: '0xrelayed', alreadyUsed: false });

    // The device's active wallet can differ from the one the order was made
    // for; the authorization must be signed by the order's wallet.
    const ACTIVE_WALLET = '0x4444444444444444444444444444444444444444';
    await expect(sendSponsoredPaycrestDeposit(order, ACTIVE_WALLET, 0)).resolves.toBe('0xrelayed');

    expect(mockSignTypedData).toHaveBeenCalledWith(
      expect.objectContaining({ chainId: 8453 }),
      expect.any(Object),
      expect.objectContaining({ to: order.depositAddress, value: '36421800' }),
      order.walletAddress,
    );
    const [, body] = mockRelay.mock.calls[0];
    expect(body).toMatchObject({ validAfter: 0, validBefore: 1790000000 });
    expect(body.nonce).toMatch(/^0x[0-9a-f]{64}$/);
    expect(mockPrepare).not.toHaveBeenCalled();
  });

  it('refuses to sign an offer that pays a different address', async () => {
    mockGetOffer.mockResolvedValue({ ...authOffer, to: '0x3333333333333333333333333333333333333333' });
    await expect(sendSponsoredPaycrestDeposit(order, SIGNER, 0)).rejects.toThrow(/don't match/);
    expect(mockSignTypedData).not.toHaveBeenCalled();
    expect(mockRelay).not.toHaveBeenCalled();
  });

  it('tops up, then sends from the wallet', async () => {
    mockGetOffer.mockResolvedValue({ mode: 'topup', chain: 'bsc', token: 'USDT' });
    mockTopUp.mockResolvedValue({ txHash: '0xgas', amountWei: '1', skipped: false });

    await expect(sendSponsoredPaycrestDeposit(order, SIGNER, 0)).resolves.toBe('0xhash');
    expect(mockTopUp).toHaveBeenCalledWith('order-1');
    expect(mockPrepare).toHaveBeenCalled();
  });

  it('propagates a top-up failure instead of self-paying with no gas', async () => {
    mockGetOffer.mockResolvedValue({ mode: 'topup', chain: 'bsc', token: 'USDT' });
    mockTopUp.mockRejectedValue(new Error('Verify your identity'));
    await expect(sendSponsoredPaycrestDeposit(order, SIGNER, 0)).rejects.toThrow(/Verify/);
    expect(mockPrepare).not.toHaveBeenCalled();
  });

  it('falls back to self-pay when the offer cannot be fetched', async () => {
    mockGetOffer.mockRejectedValue(new PaycrestError('Naira transfers are not available right now.', 404));
    await expect(sendSponsoredPaycrestDeposit(order, SIGNER, 1)).resolves.toBe('0xhash');
    expect(mockTopUp).not.toHaveBeenCalled();
    expect(mockRelay).not.toHaveBeenCalled();
  });

  it('propagates a non-404 offer error instead of self-paying', async () => {
    // A 409 here can follow a relay whose response was lost: the deposit is
    // already on its way, and self-paying would send it a second time.
    mockGetOffer.mockRejectedValue(
      new PaycrestError('This order is no longer waiting for your deposit.', 409),
    );
    await expect(sendSponsoredPaycrestDeposit(order, SIGNER, 1)).rejects.toThrow(/no longer waiting/);
    expect(mockPrepare).not.toHaveBeenCalled();
    expect(mockRelay).not.toHaveBeenCalled();
  });

  it('reuses the same signed authorization when a relay is retried', async () => {
    const retryOrder = { ...order, id: 'order-retry' };
    // Relative to the real clock: reuse depends on the authorization still
    // having more than a minute left.
    const validBefore = Math.floor(Date.now() / 1000) + 600;
    mockGetOffer.mockResolvedValue({ ...authOffer, validBefore });
    mockSignTypedData.mockResolvedValue('0x' + '11'.repeat(65));
    mockRelay
      .mockRejectedValueOnce(new PaycrestError('Already sending. Check the order in a moment.', 409))
      .mockResolvedValueOnce({ txHash: '0xrelayed', alreadyUsed: false });

    await expect(sendSponsoredPaycrestDeposit(retryOrder, SIGNER, 0)).rejects.toThrow(/Already sending/);
    await expect(sendSponsoredPaycrestDeposit(retryOrder, SIGNER, 0)).resolves.toBe('0xrelayed');

    expect(mockSignTypedData).toHaveBeenCalledTimes(1);
    const [[, first], [, second]] = mockRelay.mock.calls;
    expect(second.nonce).toBe(first.nonce);
    expect(second.signature).toBe(first.signature);
  });
});

describe('fundingMessage', () => {
  it('is null for a route that can be funded', () => {
    expect(fundingMessage({ ok: true })).toBeNull();
  });

  it('says what is short, on which network, in the words of the thing being funded', () => {
    const short = { ok: false, reason: 'short-token', token: 'USDC', chain: 'bsc', have: 1.5, need: 33.5 } as const;
    expect(fundingMessage(short)).toBe(
      'You have 1.5 USDC on BNB Smart Chain — this sale needs 33.5. Pick another network or top up.',
    );
    expect(fundingMessage(short, 'payment')).toMatch(/this payment needs 33\.5/);
  });

  it('names the gas coin when there is none', () => {
    expect(fundingMessage({ ok: false, reason: 'no-gas', nativeSymbol: 'BNB', chain: 'bsc' })).toMatch(
      /no BNB on BNB Smart Chain/,
    );
  });
});

describe('nativeBalanceFor', () => {
  const rows = [{ chainKey: 'bsc', symbol: 'BNB', balance: '0.25' }] as unknown as WalletBalanceWithChain[];

  it('reads the chain’s own coin, and 0 when the wallet holds none', () => {
    expect(nativeBalanceFor('bsc', rows)).toBe(0.25);
    expect(nativeBalanceFor('base', rows)).toBe(0);
  });

  it('is null when balances are not known', () => {
    expect(nativeBalanceFor('bsc', null)).toBeNull();
  });
});

describe('fmtTokenAmount', () => {
  it('shows two decimals from one up, six below', () => {
    expect(fmtTokenAmount(33.5)).toBe('33.5');
    expect(fmtTokenAmount(1234.567)).toBe('1,234.57');
    expect(fmtTokenAmount(0.0840001)).toBe('0.084');
  });
});
