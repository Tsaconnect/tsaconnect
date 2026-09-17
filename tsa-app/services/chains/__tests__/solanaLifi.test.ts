// Covers parseLiFiSolanaTx (splitting LiFi's base64 tx into message + required
// signers, legacy and v0) and executeLiFiSolanaSwap (signer guard → ed25519
// sign → nonevm broadcast). Signing runs for real against a fixture captured
// from a live LiFi SOL→USDC(Arbitrum) quote (Mayan route, v0 message with
// address lookup tables); network + key lookups are mocked.
import { base58, base64 } from '@scure/base';
import { ed25519 } from '@noble/curves/ed25519';
import { hexToBytes } from '@noble/hashes/utils';
import { parseLiFiSolanaTx, executeLiFiSolanaSwap } from '../solanaLifi';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('test-token'),
}));

jest.mock('../../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json', Authorization: 'Bearer test-token' }),
}));

const mockGetNetwork = jest.fn(() => 'mainnet');
jest.mock('../../../hooks/useNetwork', () => ({
  getNetwork: () => mockGetNetwork(),
}));

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
jest.mock('../../wallet', () => ({
  getMnemonic: jest.fn().mockResolvedValue(M),
}));

// Real derivation for the mocked signer, so signing still runs for real
// end-to-end (address/key pairs are genuine, only the resolution is stubbed).
const realDerivation = jest.requireActual('../derivation');
const PHANTOM_KEY: string = realDerivation.deriveSolanaPrivateKey(M);
const mockGetSolanaSigner = jest.fn();
jest.mock('../solanaWallet', () => ({
  getSolanaSigner: (...a: any[]) => mockGetSolanaSigner(...a),
}));

const mockRegister = jest.fn().mockResolvedValue(undefined);
jest.mock('../nonevmApi', () => ({
  registerNonEVMAddress: (...a: any[]) => mockRegister(...a),
}));

// Live LiFi quote fixture (Mayan, v0). Fee payer / sole required signer:
const FIXTURE_SIGNER = 'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v';
const FIXTURE_B64 =
  'AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAQAGC8b6evO+2606PWXzaqvJdDGxu+TC0vbg5HymAgNFL11hR9h+xts5WL3LfUErJnMDEDv5zl5VjLZc76BV/YP5Cc4exKx5dRVemt3hGVsILbAqLacDjoyJLepcGh8OZdmtUKkFkxS28bUd96QnLGuIP+SKR9nUPjx5KAmyEIH7KRpgX7MO9JIaGP6Q2ez2elLYn//71B04FimG9SDqdvAt5WoDBkZv5SEXMv/srbpyw5vnvIzlu8X3EmssQ5s6QAAAACg9DdKCNU/vCuOw4ifTfNicomb7F93498t8zvvk69xVOvOBlLeNNUrvx+mtjU8M/6bMkkfa3lc/YUBpqqTCXooAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAALQj5Z1paZLqq3kUJ1PAm9bYE1qpQE9jUkcSHEnSn0OVBHnVW/IxwG7udMVuzmgVB/2xst6j9I5RArHNola8E4/thJYi2wlAhp64UPJmmQu0Mh/91M4v0dxfQcuL0j/QaAoFAAUCwFwVAAUACQPA7QAAAAAAAAYCFAcJAJOr0slAxDIACAIACwwCAAAAoCUmAAAAAAAJBxUAAQIACBYACQMIAAMMAgAAAGCkdDsAAAAACQIWAwERChoAAwQXABYWARgKGgAXAAwNDg8QERITAwQWFiy7ZPrMMcSvFGCkdDsAAAAA7VuZBAAAAABkAAAAAAABAAAAoQEAAAAAECcAAQkEFgMAAAEJCQgZAAIAARkACHynZoBqeXa6TAAAAAAAAAAAAAAAABERERERERERERERERERERERERERmqGTBAAAAAAAAAAAAAAAAMKvAAAAAAAAAAAAAAAAAAADAAAAAAAAAAAAAAAAAAAApapuIXG0FuHSfsU8qME9s/kaic0AK44pIQAAAAAAAAEAAAABAtnDDfuWEY2uAzWl9dW04R5sD/Lws3UWLUj16RRlk9irAUQGKAYFMDRcCsQUbNWfqUeIq0fK5IgZkPBmSi4Je8s76zDsOHbou3kInZeem5afSZoBnA==';

// Minimal LEGACY (unversioned) tx: `numSigners` empty sig slots + message with
// header [numSigners, 0, 1], numSigners+1 account keys, a blockhash, and zero
// instructions. Signature-slot count always matches the header, as on-chain.
function legacyFixture(signerKeys: Uint8Array[]): string {
  const n = signerKeys.length;
  const msg = new Uint8Array(3 + 1 + (n + 1) * 32 + 32 + 1);
  let o = 0;
  msg[o++] = n; // numRequiredSignatures
  msg[o++] = 0; // numReadonlySigned
  msg[o++] = 1; // numReadonlyUnsigned
  msg[o++] = n + 1; // compact-u16 account count
  for (const k of signerKeys) { msg.set(k, o); o += 32; }
  msg.set(new Uint8Array(32).fill(7), o); o += 32; // trailing (program) key
  msg.set(new Uint8Array(32).fill(9), o); o += 32; // recent blockhash
  msg[o++] = 0; // zero instructions
  const tx = new Uint8Array(1 + n * 64 + msg.length);
  tx[0] = n; // signature slots
  tx.set(msg, 1 + n * 64);
  return base64.encode(tx);
}

beforeEach(() => {
  jest.clearAllMocks();
  mockGetNetwork.mockReturnValue('mainnet');
  mockGetSolanaSigner.mockResolvedValue({ address: FIXTURE_SIGNER, key: PHANTOM_KEY });
  mockRegister.mockResolvedValue(undefined);
});

describe('parseLiFiSolanaTx', () => {
  it('extracts the v0 message and its single required signer from a real LiFi tx', () => {
    const parsed = parseLiFiSolanaTx(FIXTURE_B64);
    expect(parsed.requiredSigners).toEqual([FIXTURE_SIGNER]);
    // v0 message: version byte 0x80 first, and the message must be the tx
    // minus the (1-byte count + 64-byte) signature section.
    expect(parsed.messageHex.startsWith('80')).toBe(true);
    expect(parsed.messageHex.length).toBe((base64.decode(FIXTURE_B64).length - 65) * 2);
  });

  it('parses a legacy (unversioned) message too', () => {
    const key = new Uint8Array(32).fill(3);
    const parsed = parseLiFiSolanaTx(legacyFixture([key]));
    expect(parsed.requiredSigners).toEqual([base58.encode(key)]);
    expect(parsed.messageHex.startsWith('80')).toBe(false);
  });

  it('reports every required signer when a route needs more than one', () => {
    const keys = [new Uint8Array(32).fill(3), new Uint8Array(32).fill(4)];
    const parsed = parseLiFiSolanaTx(legacyFixture(keys));
    expect(parsed.requiredSigners).toEqual(keys.map(k => base58.encode(k)));
  });

  it('throws on malformed input rather than mis-parsing', () => {
    expect(() => parseLiFiSolanaTx('')).toThrow();
    expect(() => parseLiFiSolanaTx(base64.encode(new Uint8Array([1, 2, 3])))).toThrow();
    // Claims 1 sig slot but has no message after it.
    const sigOnly = new Uint8Array(65);
    sigOnly[0] = 1;
    expect(() => parseLiFiSolanaTx(base64.encode(sigOnly))).toThrow(/no message/);
  });

  it('rejects a signature-slot count that disagrees with the message header', () => {
    const raw = base64.decode(legacyFixture([new Uint8Array(32).fill(3)]));
    raw[65] = 2; // header claims 2 required signers, but only 1 sig slot exists
    expect(() => parseLiFiSolanaTx(base64.encode(raw))).toThrow(/slot count mismatch/);
  });

  it('rejects unknown transaction versions instead of guessing their layout', () => {
    const raw = base64.decode(FIXTURE_B64);
    raw[65] = 0x81; // v1 — does not exist yet
    expect(() => parseLiFiSolanaTx(base64.encode(raw))).toThrow(/unsupported/i);
  });

  it('rejects non-canonical compact-u16 encodings', () => {
    const raw = base64.decode(legacyFixture([new Uint8Array(32).fill(3)]));
    // Rewrite the account-count byte (2) as the 2-byte non-canonical 0x82 0x00.
    const msgStart = 65;
    const patched = new Uint8Array(raw.length + 1);
    patched.set(raw.slice(0, msgStart + 3), 0);
    patched[msgStart + 3] = 0x82; // 2 with continuation bit
    patched[msgStart + 4] = 0x00; // zero continuation byte — non-canonical
    patched.set(raw.slice(msgStart + 4), msgStart + 5);
    expect(() => parseLiFiSolanaTx(base64.encode(patched))).toThrow(/non-canonical/);
  });
});

describe('executeLiFiSolanaSwap', () => {
  const params = () => ({
    txDataB64: FIXTURE_B64,
    fromAmountRaw: '1000000000',
    fromSymbol: 'SOL',
    walletAddress: '0xWALLET',
  });

  function mockFetch(body: any, ok = true, status = 200) {
    const fn = jest.fn().mockResolvedValue({ ok, status, text: async () => JSON.stringify(body) });
    global.fetch = fn as unknown as typeof fetch;
    return fn;
  }

  it('signs the message with the Solana key and broadcasts via nonevm submit-tx on mainnet', async () => {
    const fetchFn = mockFetch({ data: { txHash: '5sig...hash' } });

    const hash = await executeLiFiSolanaSwap(params());
    expect(hash).toBe('5sig...hash');

    expect(fetchFn).toHaveBeenCalledTimes(1);
    const [url, init] = fetchFn.mock.calls[0];
    expect(url).toContain('/wallet/nonevm/submit-tx?chain=solana&network=mainnet');
    const body = JSON.parse(init.body);
    const expectedMsgHex = parseLiFiSolanaTx(FIXTURE_B64).messageHex;
    expect(body.prepared).toEqual({
      family: 'solana',
      raw: expectedMsgHex,
      payloads: [{ index: 0, hashHex: expectedMsgHex, curve: 'ed25519' }],
    });
    expect(body.from).toBe(FIXTURE_SIGNER);
    expect(body.amount).toBe('1000000000');
    expect(body.symbol).toBe('SOL');

    // The signature must be a real ed25519 signature over the message by the
    // wallet's Solana key (pubkey = fee payer slot the backend fills in).
    expect(body.sigs).toHaveLength(1);
    expect(body.sigs[0].index).toBe(0);
    expect(body.sigs[0].sigHex).toMatch(/^[0-9a-f]{128}$/);
  });

  it('produces a signature that verifies against the wallet Solana pubkey', async () => {
    mockFetch({ data: { txHash: 'x' } });
    // Use the derived address for the canonical mnemonic so pubkey and key pair up.
    const { deriveSolanaAddress } = jest.requireActual('../derivation');
    const derived = deriveSolanaAddress(M);
    // Rebuild the fixture with the derived key as fee payer so the guard passes.
    const raw = base64.decode(FIXTURE_B64);
    const msgStart = 65;
    const keyOffset = msgStart + 1 + 3 + 1; // version + header + compact count (0x0b)
    raw.set(base58.decode(derived), keyOffset);
    const patched = base64.encode(raw);
    mockGetSolanaSigner.mockResolvedValue({ address: derived, key: PHANTOM_KEY });

    const fetchFn = mockFetch({ data: { txHash: 'x' } });
    await executeLiFiSolanaSwap({ ...params(), txDataB64: patched });

    const body = JSON.parse(fetchFn.mock.calls[0][1].body);
    const ok = ed25519.verify(
      hexToBytes(body.sigs[0].sigHex),
      hexToBytes(body.prepared.raw),
      base58.decode(derived),
    );
    expect(ok).toBe(true);
  });

  it('signs with the Trust Wallet path key when that is the resolved derivation', async () => {
    // A seed imported from Trust Wallet resolves to m/44'/501'/0' — the swap
    // must sign with THAT key, and the signature must verify against the
    // Trust Wallet-path pubkey embedded as fee payer.
    const twAddress: string = realDerivation.deriveSolanaAddress(M, 'trustwallet');
    const twKey: string = realDerivation.deriveSolanaPrivateKey(M, 'trustwallet');
    const raw = base64.decode(FIXTURE_B64);
    const msgStart = 65;
    const keyOffset = msgStart + 1 + 3 + 1;
    raw.set(base58.decode(twAddress), keyOffset);
    mockGetSolanaSigner.mockResolvedValue({ address: twAddress, key: twKey });

    const fetchFn = mockFetch({ data: { txHash: 'x' } });
    await executeLiFiSolanaSwap({ ...params(), txDataB64: base64.encode(raw) });

    const body = JSON.parse(fetchFn.mock.calls[0][1].body);
    expect(body.from).toBe(twAddress);
    const ok = ed25519.verify(
      hexToBytes(body.sigs[0].sigHex),
      hexToBytes(body.prepared.raw),
      base58.decode(twAddress),
    );
    expect(ok).toBe(true);
  });

  it('refuses to execute in testnet mode (broadcast is pinned to mainnet)', async () => {
    mockGetNetwork.mockReturnValue('testnet');
    const fetchFn = mockFetch({ data: { txHash: 'x' } });
    await expect(executeLiFiSolanaSwap(params())).rejects.toThrow(/mainnet-only/);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it('refuses to sign when the wallet is not the required signer', async () => {
    mockGetSolanaSigner.mockResolvedValue({
      address: 'SomeOtherAddress1111111111111111111111111111',
      key: PHANTOM_KEY,
    });
    const fetchFn = mockFetch({ data: { txHash: 'x' } });
    await expect(executeLiFiSolanaSwap(params())).rejects.toThrow(/does not support/i);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it('registers the Solana address (best-effort) before broadcasting', async () => {
    mockFetch({ data: { txHash: 'x' } });
    await executeLiFiSolanaSwap(params());
    expect(mockRegister).toHaveBeenCalledWith('0xWALLET', 'solana', FIXTURE_SIGNER);
  });

  it('still broadcasts when registration fails (idempotent upsert elsewhere)', async () => {
    mockRegister.mockRejectedValue(new Error('offline'));
    mockFetch({ data: { txHash: 'ok' } });
    await expect(executeLiFiSolanaSwap(params())).resolves.toBe('ok');
  });

  it('surfaces the backend message when the broadcast is rejected', async () => {
    mockFetch({ message: 'sender address is not registered to this account' }, false, 403);
    await expect(executeLiFiSolanaSwap(params())).rejects.toThrow(/not registered/);
  });
});
