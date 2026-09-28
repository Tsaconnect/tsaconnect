// services/walletProof.ts
//
// Proof of wallet ownership. The API only links a wallet to the account after
// the account signs a server-issued challenge with that wallet's key: the
// backend spends Spendable MCGP from the linked wallet with no signature from
// the user (IT sell, bills, marketplace checkout), so linking a wallet nobody
// proved is linking someone else's money.
//
// Flow: POST /wallet/challenge {walletAddress} → {proofRequired, nonce, message}
// → sign `message` (personal_sign) with the wallet's local key → send
// {walletNonce, walletSignature} with POST /wallet/register. A wallet the
// account already proved answers proofRequired:false and needs no signature.
//
// Kept free of network and storage imports so the logic is testable; the
// wiring lives in walletApi.ts.

/** The account has not proven it owns the wallet this request needs. */
export const WALLET_PROOF_REQUIRED = 'WALLET_PROOF_REQUIRED';
/** A proof was sent but refused (expired, used, wrong key). */
export const WALLET_PROOF_INVALID = 'WALLET_PROOF_INVALID';
/** This device holds no key for the wallet, so it cannot prove ownership. */
export const WALLET_KEY_UNAVAILABLE = 'WALLET_KEY_UNAVAILABLE';

export const WALLET_KEY_UNAVAILABLE_MESSAGE =
  "This wallet's key isn't on this device, so it can't be linked to your account. " +
  'Import it from its recovery phrase, or switch to a wallet created on this device.';

export interface WalletChallenge {
  proofRequired: boolean;
  walletAddress: string;
  nonce?: string;
  message?: string;
  expiresAt?: string;
}

export interface WalletProofFields {
  walletNonce: string;
  walletSignature: string;
}

export type WalletProofOutcome =
  | { ok: true; proof?: WalletProofFields }
  | { ok: false; code?: string; message: string };

interface ChallengeResponse {
  success: boolean;
  data?: WalletChallenge;
  message?: string;
  status?: number;
}

export interface WalletProofDeps {
  fetchChallenge: (address: string) => Promise<ChallengeResponse>;
  /** personal_sign `message` with `address`'s key; throws when there is no key. */
  sign: (message: string, address: string) => Promise<string>;
  /** The signed-in account's user id — the only account a proof may be for. */
  accountId: () => Promise<string | null>;
}

const UNVERIFIABLE = 'Could not verify this wallet. Please try again.';

// The exact message the API issues (tsa-api-go handlers/wallet_proof.go
// walletProofMessage). A personal_sign signature proves ownership to whoever
// asks, so the app signs nothing that is not this template, for this account
// and this wallet — a look-alike request from anywhere else is refused.
const HEADER = 'TSA Connect: link wallet to account';
const BODY =
  'Sign this message to prove you own this wallet and link it to your TSA Connect account. ' +
  'It costs no gas and does not move any funds.';
const FOOTER = 'tsa.mcgpchain.com — only sign this inside the TSA Connect app';
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const TEMPLATE = new RegExp(
  `^${escapeRe(HEADER)}\\n\\n${escapeRe(BODY)}\\n\\n` +
    'Wallet: (0x[0-9a-fA-F]{40})\\n' +
    'Account: ([0-9a-fA-F-]{36})\\n' +
    'User: ([^\\n]+)\\n' +
    'Purpose: link-wallet\\n' +
    'Nonce: ([0-9a-f]{64})\\n' +
    'Expires: ([0-9T:.Z+-]+)\\n\\n' +
    `${escapeRe(FOOTER)}$`,
);

export interface WalletProofMessage {
  wallet: string;
  account: string;
  user: string;
  nonce: string;
  expires: string;
}

/** Parse a wallet-link challenge message; null unless it is exactly the template. */
export function parseWalletProofMessage(message: string): WalletProofMessage | null {
  const m = TEMPLATE.exec(message);
  if (!m) return null;
  return { wallet: m[1], account: m[2], user: m[3], nonce: m[4], expires: m[5] };
}

/** The `userId` claim of the API's access token (payload is base64url JSON). */
export function jwtUserId(token: string | null | undefined): string | null {
  if (!token) return null;
  const raw = token.startsWith('Bearer ') ? token.slice('Bearer '.length) : token;
  const payload = raw.split('.')[1];
  if (!payload) return null;
  try {
    const b64 = payload.replace(/-/g, '+').replace(/_/g, '/');
    const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
    const claims = JSON.parse(globalThis.atob(padded));
    return typeof claims?.userId === 'string' ? claims.userId : null;
  } catch {
    return null;
  }
}

/**
 * Get the signed proof POST /wallet/register needs for `address`. Resolves
 * `{ok: true}` with no proof when none is needed: the account already proved
 * this wallet, or the API predates proofs (no challenge route — the app ships
 * before the API, and must keep linking wallets against the old one).
 */
export async function proveWalletOwnership(
  address: string,
  deps: WalletProofDeps,
): Promise<WalletProofOutcome> {
  const res = await deps.fetchChallenge(address);
  if (!res.success) {
    if (res.status === 404) return { ok: true };
    return { ok: false, message: res.message || UNVERIFIABLE };
  }
  const challenge = res.data;
  if (challenge && challenge.proofRequired === false) return { ok: true };
  const { nonce, message } = challenge ?? {};
  const parsed = message ? parseWalletProofMessage(message) : null;
  const account = await deps.accountId();
  if (
    !nonce ||
    !message ||
    !parsed ||
    !account ||
    parsed.wallet.toLowerCase() !== address.toLowerCase() ||
    parsed.account.toLowerCase() !== account.toLowerCase() ||
    parsed.nonce !== nonce
  ) {
    console.warn('[walletProof] refused to sign a challenge that is not for this account and wallet');
    return { ok: false, message: UNVERIFIABLE };
  }
  let signature: string;
  try {
    signature = await deps.sign(message, address);
  } catch (err) {
    console.warn('[walletProof] could not sign the wallet challenge:', err);
    return { ok: false, code: WALLET_KEY_UNAVAILABLE, message: WALLET_KEY_UNAVAILABLE_MESSAGE };
  }
  return { ok: true, proof: { walletNonce: nonce, walletSignature: signature } };
}

type Refusable = { success: boolean; code?: string; message?: string };

/**
 * Run `call`; if the API refuses it because the account's wallet is not proven
 * (WALLET_PROOF_REQUIRED), prove and link the device's wallet with `relink`,
 * then retry once. The API refuses before acting, so the retry cannot double
 * anything. When linking fails, the refusal carries the linking error instead
 * — it says what the user can actually do (e.g. the key isn't on this device).
 */
export async function retryAfterWalletProof<R extends Refusable>(
  call: () => Promise<R>,
  relink: () => Promise<Refusable>,
): Promise<R> {
  const first = await call();
  if (first.success || first.code !== WALLET_PROOF_REQUIRED) return first;
  const linked = await relink();
  if (!linked.success) {
    return { ...first, code: linked.code ?? first.code, message: linked.message || first.message };
  }
  return call();
}
