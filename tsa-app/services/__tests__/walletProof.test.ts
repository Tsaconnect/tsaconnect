// Proof of wallet ownership: the API links a wallet to the account only after
// the account signs a server-issued challenge with that wallet's key, because
// the operator spends Spendable MCGP from the linked wallet unsigned.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn(),
}));

import { ethers } from 'ethers';
import { signMessage } from '../wallet';
import {
  jwtUserId,
  parseWalletProofMessage,
  proveWalletOwnership,
  retryAfterWalletProof,
  WALLET_KEY_UNAVAILABLE,
  WALLET_PROOF_REQUIRED,
  type WalletChallenge,
  type WalletProofDeps,
} from '../walletProof';

const WALLET = ethers.Wallet.createRandom();
const ACCOUNT = '5f0c3a52-7a1e-4d1b-9f7e-3c2b1a0d9e8f';
const NONCE = 'ab'.repeat(32);

/** The API's message, byte for byte (tsa-api-go walletProofMessage). */
function messageFor(wallet: string, { account = ACCOUNT, nonce = NONCE, user = 'ojay' } = {}) {
  return (
    'TSA Connect: link wallet to account\n\n' +
    'Sign this message to prove you own this wallet and link it to your TSA Connect account. ' +
    'It costs no gas and does not move any funds.\n\n' +
    `Wallet: ${wallet}\nAccount: ${account}\nUser: ${user}\nPurpose: link-wallet\n` +
    `Nonce: ${nonce}\nExpires: 2026-09-28T12:10:00Z\n\n` +
    'tsa.mcgpchain.com — only sign this inside the TSA Connect app'
  );
}

function challengeFor(wallet: string, opts: Parameters<typeof messageFor>[1] = {}): WalletChallenge {
  return { proofRequired: true, walletAddress: wallet, nonce: opts.nonce ?? NONCE, message: messageFor(wallet, opts) };
}

function deps(overrides: Partial<WalletProofDeps>): WalletProofDeps {
  return {
    fetchChallenge: async () => ({ success: true, data: challengeFor(WALLET.address) }),
    sign: jest.fn(),
    accountId: async () => ACCOUNT,
    ...overrides,
  };
}

describe('proveWalletOwnership', () => {
  it('signs the server challenge with the wallet key, and the signature recovers to the wallet', async () => {
    const challenge = challengeFor(WALLET.address);
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({
        fetchChallenge: async () => ({ success: true, data: challenge }),
        // The real signer, handed the key as the create/import flows do.
        sign: (message, address) => signMessage(message, address, WALLET.privateKey),
      }),
    );

    expect(out.ok).toBe(true);
    if (!out.ok || !out.proof) throw new Error('expected a proof');
    expect(out.proof.walletNonce).toBe(NONCE);
    expect(ethers.verifyMessage(challenge.message!, out.proof.walletSignature)).toBe(WALLET.address);
  });

  it('needs no signature for a wallet the account already proved', async () => {
    const sign = jest.fn();
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({
        fetchChallenge: async () => ({ success: true, data: { proofRequired: false, walletAddress: WALLET.address } }),
        sign,
      }),
    );
    expect(out).toEqual({ ok: true });
    expect(sign).not.toHaveBeenCalled();
  });

  it('links without a proof against an API that predates the challenge route', async () => {
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({ fetchChallenge: async () => ({ success: false, status: 404, message: 'Not found' }) }),
    );
    expect(out).toEqual({ ok: true });
  });

  it('surfaces any other challenge failure', async () => {
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({ fetchChallenge: async () => ({ success: false, status: 500, message: 'Could not start wallet verification' }) }),
    );
    expect(out).toEqual({ ok: false, message: 'Could not start wallet verification' });
  });

  // A personal_sign signature proves ownership to whoever asks: anything that
  // is not exactly our template, for this account and this wallet, is refused.
  it.each([
    ['another wallet', () => challengeFor(ethers.Wallet.createRandom().address)],
    ['another account', () => challengeFor(WALLET.address, { account: '00000000-0000-4000-8000-000000000000' })],
    [
      'a nonce that is not the challenge nonce',
      () => ({ ...challengeFor(WALLET.address), nonce: 'cd'.repeat(32) }),
    ],
    [
      'a message that merely contains the address and nonce',
      () => ({
        ...challengeFor(WALLET.address),
        message: `Log in to example.xyz\nWallet: ${WALLET.address}\nNonce: ${NONCE}`,
      }),
    ],
    [
      'the template without its "only sign this inside the app" line',
      () => {
        const c = challengeFor(WALLET.address);
        return { ...c, message: c.message!.replace(/\n\ntsa\.mcgpchain\.com.*$/, '') };
      },
    ],
    [
      'a different purpose',
      () => {
        const c = challengeFor(WALLET.address);
        return { ...c, message: c.message!.replace('Purpose: link-wallet', 'Purpose: withdraw') };
      },
    ],
  ])('refuses to sign %s', async (_name, make) => {
    const sign = jest.fn();
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({ fetchChallenge: async () => ({ success: true, data: make() }), sign }),
    );
    expect(out.ok).toBe(false);
    expect(sign).not.toHaveBeenCalled();
  });

  it('refuses to sign when it cannot tell which account is signed in', async () => {
    const sign = jest.fn();
    const out = await proveWalletOwnership(WALLET.address, deps({ sign, accountId: async () => null }));
    expect(out.ok).toBe(false);
    expect(sign).not.toHaveBeenCalled();
  });

  it('explains a watch-only wallet (no key on this device) instead of failing blind', async () => {
    const out = await proveWalletOwnership(
      WALLET.address,
      deps({
        sign: async () => {
          throw new Error(`No private key stored for wallet ${WALLET.address}.`);
        },
      }),
    );
    expect(out.ok).toBe(false);
    if (out.ok) return;
    expect(out.code).toBe(WALLET_KEY_UNAVAILABLE);
    expect(out.message).toMatch(/key isn't on this device/);
  });

  it('refuses a key that does not derive to the wallet being linked', async () => {
    const stranger = ethers.Wallet.createRandom();
    await expect(signMessage('hi', WALLET.address, stranger.privateKey)).rejects.toThrow(/key mismatch/i);
  });
});

describe('parseWalletProofMessage', () => {
  it('reads every field of the template', () => {
    expect(parseWalletProofMessage(messageFor(WALLET.address, { user: 'o***@gmail.com' }))).toEqual({
      wallet: WALLET.address,
      account: ACCOUNT,
      user: 'o***@gmail.com',
      nonce: NONCE,
      expires: '2026-09-28T12:10:00Z',
    });
  });

  it('rejects an extra line smuggled in anywhere', () => {
    const m = messageFor(WALLET.address).replace('Purpose: link-wallet', 'Purpose: link-wallet\nAlso: approve');
    expect(parseWalletProofMessage(m)).toBeNull();
  });
});

describe('jwtUserId', () => {
  const token = (claims: object) =>
    `h.${Buffer.from(JSON.stringify(claims)).toString('base64url')}.s`;

  it('reads the userId claim, with or without the Bearer prefix', () => {
    expect(jwtUserId(token({ userId: ACCOUNT }))).toBe(ACCOUNT);
    expect(jwtUserId(`Bearer ${token({ userId: ACCOUNT })}`)).toBe(ACCOUNT);
  });

  it('is null for anything else', () => {
    expect(jwtUserId(null)).toBeNull();
    expect(jwtUserId('not-a-jwt')).toBeNull();
    expect(jwtUserId(token({ sub: ACCOUNT }))).toBeNull();
  });
});

describe('retryAfterWalletProof', () => {
  it('passes a success straight through without linking', async () => {
    const relink = jest.fn();
    const out = await retryAfterWalletProof(async () => ({ success: true }), relink);
    expect(out).toEqual({ success: true });
    expect(relink).not.toHaveBeenCalled();
  });

  it('does not relink for an unrelated refusal', async () => {
    const relink = jest.fn();
    const out = await retryAfterWalletProof(async () => ({ success: false, code: 'it_agreement_required' }), relink);
    expect(out.code).toBe('it_agreement_required');
    expect(relink).not.toHaveBeenCalled();
  });

  it('proves the wallet, then retries once', async () => {
    const call = jest
      .fn()
      .mockResolvedValueOnce({ success: false, code: WALLET_PROOF_REQUIRED, message: 'Confirm you own this wallet' })
      .mockResolvedValueOnce({ success: true });
    const relink = jest.fn().mockResolvedValue({ success: true });

    const out = await retryAfterWalletProof(call, relink);
    expect(out).toEqual({ success: true });
    expect(relink).toHaveBeenCalledTimes(1);
    expect(call).toHaveBeenCalledTimes(2);
  });

  it('never loops: a second refusal is returned as-is', async () => {
    const refusal = { success: false, code: WALLET_PROOF_REQUIRED, message: 'Confirm you own this wallet' };
    const call = jest.fn().mockResolvedValue(refusal);
    const out = await retryAfterWalletProof(call, async () => ({ success: true }));
    expect(out).toEqual(refusal);
    expect(call).toHaveBeenCalledTimes(2);
  });

  it("reports why linking failed, e.g. the wallet's key isn't on this device", async () => {
    const call = jest.fn().mockResolvedValue({ success: false, code: WALLET_PROOF_REQUIRED, message: 'Confirm…' });
    const out = await retryAfterWalletProof(call, async () => ({
      success: false,
      code: WALLET_KEY_UNAVAILABLE,
      message: "This wallet's key isn't on this device",
    }));
    expect(out).toMatchObject({ success: false, code: WALLET_KEY_UNAVAILABLE, message: "This wallet's key isn't on this device" });
    expect(call).toHaveBeenCalledTimes(1);
  });
});
