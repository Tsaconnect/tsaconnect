// Covers the Bitcoin-source LiFi swap path: reading LiFi's deposit
// instructions (address, sats, OP_RETURN memo) out of a transactionRequest and
// handing them to the BTC prepare/sign/submit flow. Every assertion here is
// about failing CLOSED — a deposit sent with the wrong amount, or without its
// memo, is unrecoverable, so a transactionRequest we cannot read exactly must
// abort rather than guess.
import { parseLiFiSats, parseLiFiBtcMemo, parseLiFiBtcPayload, executeLiFiBtcSwap } from '../btcLifi';

const mockGetNetwork = jest.fn(() => 'mainnet');
jest.mock('../../../hooks/useNetwork', () => ({
  getNetwork: () => mockGetNetwork(),
}));

const mockGetBtcAddress = jest.fn();
jest.mock('../btcWallet', () => ({
  getBtcAddress: (...a: any[]) => mockGetBtcAddress(...a),
}));

const mockPrepareBtc = jest.fn();
const mockBroadcastBtc = jest.fn();
jest.mock('../btcSend', () => ({
  prepareBtc: (...a: any[]) => mockPrepareBtc(...a),
  broadcastBtc: (...a: any[]) => mockBroadcastBtc(...a),
}));

const mockRegister = jest.fn().mockResolvedValue(undefined);
jest.mock('../nonevmApi', () => ({
  registerNonEVMAddress: (...a: any[]) => mockRegister(...a),
}));

const WALLET = '0x1111111111111111111111111111111111111111';
const BTC_ADDR = 'bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu';
const DEPOSIT = 'bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4';
// "=:ETH.ETH:0xdead" — a THORChain-style swap memo, hex-encoded.
const MEMO_HEX = '3d3a4554482e4554483a307864656164';

const PREPARED = { family: 'btc', raw: '{}', payloads: [{ index: 0, hashHex: 'ab'.repeat(32), curve: 'secp256k1' }] };

beforeEach(() => {
  jest.clearAllMocks();
  mockGetNetwork.mockReturnValue('mainnet');
  mockGetBtcAddress.mockResolvedValue(BTC_ADDR);
  mockPrepareBtc.mockResolvedValue({ prepared: PREPARED, fee: { estFeeRaw: '2240' } });
  mockBroadcastBtc.mockResolvedValue({ txHash: 'dd'.repeat(32) });
});

describe('parseLiFiSats', () => {
  it('reads both hex-quantity and decimal base units', () => {
    expect(parseLiFiSats('0x1e848')).toBe(125000n);
    expect(parseLiFiSats('125000')).toBe(125000n);
    expect(parseLiFiSats(125000)).toBe(125000n);
  });

  it('refuses anything that is not a whole number of sats', () => {
    // A fractional value would mean LiFi quoted BTC, not sats — 1e8 off.
    expect(() => parseLiFiSats('0.00125')).toThrow();
    expect(() => parseLiFiSats('')).toThrow();
    expect(() => parseLiFiSats(undefined)).toThrow();
    expect(() => parseLiFiSats('0')).toThrow();
  });

  // JSON hands us a number, not a string, whenever LiFi omits the quotes. That
  // path used to reach BigInt() unguarded, so a fractional value surfaced
  // "The number 0.00125 cannot be converted to a BigInt because it is not an
  // integer" in the swap UI.
  it('reports a fractional JSON number in its own words, not the engine\'s', () => {
    expect(() => parseLiFiSats(0.00125 as never)).toThrow(/deposit amount/i);
    // Integral numbers still parse. (Only values ≥ 1e21 stringify to
    // exponential form and get rejected — five orders of magnitude past the
    // 2.1e15 sats that will ever exist, so no real deposit reaches it.)
    expect(parseLiFiSats(2_100_000_000_000_000 as never)).toBe(2_100_000_000_000_000n);
  });
});

describe('parseLiFiBtcMemo', () => {
  it('normalizes a hex memo and treats an empty one as no memo', () => {
    expect(parseLiFiBtcMemo('0x' + MEMO_HEX.toUpperCase())).toBe(MEMO_HEX);
    expect(parseLiFiBtcMemo(MEMO_HEX)).toBe(MEMO_HEX);
    expect(parseLiFiBtcMemo('0x')).toBe('');
    expect(parseLiFiBtcMemo(undefined)).toBe('');
  });

  it('refuses a memo it cannot put in an OP_RETURN', () => {
    expect(() => parseLiFiBtcMemo('0xzzzz')).toThrow();
    expect(() => parseLiFiBtcMemo('0xabc')).toThrow();
    expect(() => parseLiFiBtcMemo('0x' + 'ab'.repeat(81))).toThrow();
  });
});

// A live LiFi BTC→USDC quote (2026-09-07, Layerswap) returns its `data` as a
// whole PSBT built from the sender's UTXOs — not a memo. Truncated here; the
// backend holds the verbatim fixture and validates it output by output.
const FIXTURE_PSBT = '70736274ff0100b80200000001450e2079d46908fb35671806e18796442c0647748d5a84897b7976b6d6607179020000';

describe('parseLiFiBtcPayload', () => {
  it('recognises a pre-built transaction by its BIP-174 magic', () => {
    expect(parseLiFiBtcPayload('0x' + FIXTURE_PSBT)).toEqual({ kind: 'psbt', psbtHex: FIXTURE_PSBT });
    expect(parseLiFiBtcPayload(FIXTURE_PSBT.toUpperCase())).toEqual({ kind: 'psbt', psbtHex: FIXTURE_PSBT });
  });

  it('still reads a short tag as a memo for the bridges that use one', () => {
    expect(parseLiFiBtcPayload('0x' + MEMO_HEX)).toEqual({ kind: 'memo', memoHex: MEMO_HEX });
    expect(parseLiFiBtcPayload('0x')).toEqual({ kind: 'memo', memoHex: '' });
    expect(parseLiFiBtcPayload(undefined)).toEqual({ kind: 'memo', memoHex: '' });
  });

  // A PSBT is far past the 80-byte OP_RETURN limit, so before this existed
  // every live BTC route was rejected as "a deposit tag too large for Bitcoin".
  it('does not mistake a transaction for an oversized memo', () => {
    expect(() => parseLiFiBtcPayload('0x' + FIXTURE_PSBT)).not.toThrow();
  });
});

describe('executeLiFiBtcSwap', () => {
  const run = (txRequest: any, fromAmountRaw = '125000') =>
    executeLiFiBtcSwap({ txRequest, fromAmountRaw, walletAddress: WALLET });

  it('pays the deposit address the quoted sats with the route memo attached', async () => {
    const hash = await run({ to: DEPOSIT, value: '0x1e848', data: '0x' + MEMO_HEX });

    expect(hash).toBe('dd'.repeat(32));
    expect(mockPrepareBtc).toHaveBeenCalledWith(expect.objectContaining({
      from: BTC_ADDR,
      to: DEPOSIT,
      amount: '125000',
      symbol: 'BTC',
      memoHex: MEMO_HEX,
    }));
    // Same params object must reach the broadcast, or the audit row and the
    // signed tx would disagree about what was sent.
    expect(mockBroadcastBtc).toHaveBeenCalledWith(PREPARED, expect.objectContaining({
      to: DEPOSIT, amount: '125000', memoHex: MEMO_HEX,
    }));
    // submit-tx only broadcasts for registered addresses.
    expect(mockRegister).toHaveBeenCalledWith(WALLET, 'btc', BTC_ADDR);
  });

  it('supports memo-less routes (deposit-address bridges)', async () => {
    await run({ to: DEPOSIT, value: '125000' });
    expect(mockPrepareBtc).toHaveBeenCalledWith(expect.objectContaining({ memoHex: '' }));
  });

  // The live route shape: LiFi already picked the UTXOs and built the spend, so
  // it is forwarded for signing rather than reconstructed from to/value.
  it('forwards a pre-built transaction for signing instead of building one', async () => {
    await run({ to: DEPOSIT, value: '125000', data: '0x' + FIXTURE_PSBT });

    const params = mockPrepareBtc.mock.calls[0][0];
    expect(params.psbtHex).toBe(FIXTURE_PSBT);
    expect(params).not.toHaveProperty('memoHex');
    // to/amount still travel: the backend checks the transaction pays this
    // deposit address, and no more than this amount, before signing anything.
    expect(params).toMatchObject({ to: DEPOSIT, amount: '125000', from: BTC_ADDR });
  });

  // The address, the signing key and the broadcast all have to agree on one
  // network. Pinning it in the params — rather than leaving prepare/sign/submit
  // to re-read the app toggle three times — is what keeps them from drifting
  // apart if the toggle flips mid-swap.
  it('pins every leg of the deposit to mainnet', async () => {
    await run({ to: DEPOSIT, value: '125000' });
    expect(mockPrepareBtc).toHaveBeenCalledWith(expect.objectContaining({ network: 'mainnet' }));
    expect(mockBroadcastBtc).toHaveBeenCalledWith(PREPARED, expect.objectContaining({ network: 'mainnet' }));
  });

  it('refuses a route asking for a different amount than the user quoted', async () => {
    await expect(run({ to: DEPOSIT, value: '999999', data: '0x' + MEMO_HEX }))
      .rejects.toThrow(/changed the amount/i);
    expect(mockPrepareBtc).not.toHaveBeenCalled();
  });

  it('refuses a route with no deposit address', async () => {
    await expect(run({ value: '125000', data: '0x' })).rejects.toThrow(/deposit address/i);
    expect(mockPrepareBtc).not.toHaveBeenCalled();
  });

  it('refuses a memo it cannot encode rather than dropping it', async () => {
    await expect(run({ to: DEPOSIT, value: '125000', data: '0x' + 'ab'.repeat(81) }))
      .rejects.toThrow();
    expect(mockPrepareBtc).not.toHaveBeenCalled();
  });

  it('refuses to sign a mainnet route while the app is on testnet', async () => {
    mockGetNetwork.mockReturnValue('testnet');
    await expect(run({ to: DEPOSIT, value: '125000' })).rejects.toThrow(/mainnet-only/i);
    expect(mockPrepareBtc).not.toHaveBeenCalled();
  });

  it('refuses when the wallet has no Bitcoin key', async () => {
    mockGetBtcAddress.mockResolvedValue(null);
    await expect(run({ to: DEPOSIT, value: '125000' })).rejects.toThrow(/no bitcoin key/i);
    expect(mockPrepareBtc).not.toHaveBeenCalled();
  });
});
