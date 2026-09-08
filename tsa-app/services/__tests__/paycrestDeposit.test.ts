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
jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSignTransaction(...a),
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

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { checkFunding, sendPaycrestDeposit } from '../paycrestDeposit';
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

  const prepared = {
    to: '0xusdc',
    data: '0xa9059cbb',
    value: '0',
    gasLimit: '65000',
    gasPrice: '1000000000', // 1 gwei → 0.000065 ETH
    nonce: 7,
    chainId: 8453,
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

  it('surfaces the server message when the broadcast is rejected', async () => {
    mockSubmit.mockResolvedValue({ success: false, message: 'insufficient funds for transfer' });

    await expect(sendPaycrestDeposit(order, '0xfrom')).rejects.toThrow(
      'insufficient funds for transfer',
    );
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
