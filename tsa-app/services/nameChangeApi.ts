// services/nameChangeApi.ts
//
// A verified user's name change (/api/users/me/name-change): her name is the
// one on her ID, so changing it means paying the re-verification fee and
// verifying again — the new document's name becomes her name. See
// docs/specs/2026-09-28-name-change-rekyc.md. An unverified user edits her
// name on the profile form instead (PUT /users/profile).
//
// A paid change starts with her proving the paying wallet is hers: she signs
// the server's challenge (walletProof) with it, and that wallet is then the
// only one the fee is taken from — and the only one a refund can go to.
import { ApiError, createApiRequest } from '@/services/apiRequest';

export type NameChangeStatus =
  | 'paying'
  | 'paid'
  | 'verifying'
  | 'declined'
  | 'held_for_review'
  | 'approved'
  | 'rejected'
  | 'lapsed'
  | 'cancelled';

export type NameChangePayMethod = 'mcgp_spendable' | 'stablecoin' | 'free';

/** Where a paid fee stands: none (unpaid, or free), held/received, kept, or on its way back. */
export type NameChangeFeeStatus =
  | 'none' | 'held' | 'received' | 'settled' | 'reversed'
  | 'refund_sending' | 'refunded' | 'refund_failed' | 'refund_uncertain';

/**
 * The stablecoin payment she is to make. Amounts come twice: `amount` is a
 * whole-token decimal string for reading, `amountBaseUnits` the exact figure a
 * transfer is built and checked against.
 */
export type StablecoinTransfer = {
  chain: string;
  chainId: number;
  token: string;
  tokenAddress: string;
  /** The proven wallet: the only one the fee is accepted from. */
  from: string;
  /** The treasury. */
  to: string;
  amount: string;
  amountBaseUnits: string;
  decimals: number;
};

export type NameChange = {
  id: string;
  status: NameChangeStatus;
  /** A decimal string, e.g. "1.00". */
  feeUsd: string;
  payMethod: NameChangePayMethod;
  payToken: string;
  payChain: string;
  payAmount: string;
  payAmountBaseUnits: string;
  payDecimals: number;
  /** The proven wallet the fee comes from (and any refund goes to). */
  payFrom?: string;
  feeStatus: NameChangeFeeStatus;
  /** Verification sessions started so far. */
  attempts: number;
  maxAttempts: number;
  expiresAt: string;
  oldName: string;
  newVerifiedName?: string;
  matchStrength?: 'exact' | 'partial' | 'none';
  /** The one line about this change meant for her (a review outcome, a refund…). */
  message?: string;
  /** While a stablecoin fee is still to be paid: the transfer to make. */
  transfer?: StablecoinTransfer;
  completedAt?: string;
  createdAt: string;
  updatedAt: string;
};

export type NameChangeBlocker = { code: string; message: string };

/** One stablecoin the fee can be paid in. `chain` is our chain key ('base', 'bsc'…). */
export type StablecoinOption = {
  chain: string;
  chainId: number;
  token: string;
  tokenAddress: string;
  amount: string;
  amountBaseUnits: string;
  decimals: number;
};

export type McgpMethodQuote = {
  method: 'mcgp_spendable';
  available: boolean;
  /** Where spendable MCGP lives: 'sonic'. The wallet proof is for this chain. */
  chain: string;
  token: 'MCGP';
  amount?: string;
  amountBaseUnits?: string;
  decimals: number;
  /** The spendable MCGP of the wallet the overview was asked about. */
  balance?: string;
  balanceBaseUnits?: string;
};
export type StablecoinMethodQuote = { method: 'stablecoin'; available: boolean; options: StablecoinOption[] };
export type NameChangeMethodQuote = McgpMethodQuote | StablecoinMethodQuote;

export type NameChangeQuote = { feeUsd: string; methods: NameChangeMethodQuote[] };

export type NameChangeOverview = {
  enabled: boolean;
  /** Why no change can be started at all; absent when one can (subject to eligible). */
  unavailableReason?: 'disabled' | 'not_verified';
  /** The open change — or, with none open, the latest one — or null. */
  change: NameChange | null;
  /** She may start a new change now. */
  eligible: boolean;
  /** Money in flight under her current name: finish these first. */
  blockers: NameChangeBlocker[];
  /** Past the yearly limit: when the next change becomes possible. */
  nextAllowedAt?: string;
  limits: { maxPerYear: number; completedInLastYear: number; maxAttempts: number; ttlDays: number };
  /** The fee for a new change: null while one is open, or when none can be started. */
  quote: NameChangeQuote | null;
};

/** The challenge the paying wallet signs (personal_sign). */
export type WalletProof = { message: string; nonce: string; wallet: string; chain: string; expiresAt: string };

export type NameChangeStart = { change: NameChange; transfer?: StablecoinTransfer };

/** How she pays, with the signed proof for the paying wallet. A free change needs only her password. */
export type NameChangePay =
  | { payMethod: 'free'; password: string }
  | {
      payMethod: 'mcgp_spendable' | 'stablecoin';
      chain?: string;
      token?: string;
      walletAddress: string;
      nonce: string;
      signature: string;
      password: string;
    };

/** Machine codes the name-change routes answer with — keyed on, never the words beside them. */
export const NAME_CHANGE_CODES = {
  DISABLED: 'name_change_disabled',
  NOT_VERIFIED: 'not_verified',
  USER_NOT_FOUND: 'user_not_found',
  CHANGE_OPEN: 'change_open',
  NO_OPEN_CHANGE: 'no_open_change',
  INVALID_REQUEST: 'invalid_request',
  LIMIT: 'name_change_limit',
  CANCEL_LIMIT: 'cancel_limit',
  MONEY_IN_FLIGHT: 'money_in_flight',
  WALLET_PROOF_INVALID: 'wallet_proof_invalid',
  PASSWORD_INCORRECT: 'password_incorrect',
  ACCOUNT_LOCKED: 'account_locked',
  METHOD_UNAVAILABLE: 'method_unavailable',
  INSUFFICIENT_BALANCE: 'insufficient_balance',
  PAYMENT_FAILED: 'payment_failed',
  /** The fee transaction isn't mined, or deep enough, yet: keep checking. */
  PAYMENT_PENDING_CHAIN: 'payment_pending_chain',
  /** The transaction can never pay this change (reverted, wrong recipient or sender, too early). */
  PAYMENT_MISMATCH: 'payment_mismatch',
  /** A payment (or MCGP debit) is still settling: not now. */
  PAYMENT_PENDING: 'payment_pending',
  ATTEMPTS_EXHAUSTED: 'attempts_exhausted',
  SESSION_LIMIT: 'session_limit',
  SESSION_PENDING: 'session_pending',
  CHANGE_LAPSED: 'change_lapsed',
  UNDER_REVIEW: 'under_review',
  CANCEL_NOT_ALLOWED: 'cancel_not_allowed',
  BAD_STATE: 'bad_state',
  /** PUT /users/profile: a verified user's name changes only through this flow. */
  USE_NAME_CHANGE: 'use_name_change',
} as const;

/** A refusal, with what it carries: the blockers, or when she may try again. */
export class NameChangeError extends ApiError {
  blockers: NameChangeBlocker[];
  nextAllowedAt?: string;
  constructor(message: string, status: number, code?: string, body?: unknown) {
    super(message, status, code);
    this.name = 'NameChangeError';
    const data = (body as { data?: { blockers?: unknown; nextAllowedAt?: unknown } } | null)?.data;
    this.blockers = Array.isArray(data?.blockers) ? (data.blockers as NameChangeBlocker[]) : [];
    this.nextAllowedAt = typeof data?.nextAllowedAt === 'string' ? data.nextAllowedAt : undefined;
  }
}

/** Another TSA Connect account holds the wallet (the server's wallet claim answers 409 with no code). */
export const isWalletConflict = (e: unknown): boolean =>
  e instanceof ApiError && e.status === 409 && !e.code;

export const WALLET_CONFLICT =
  'This wallet is linked to another TSA Connect account. Switch to your own wallet on this phone to pay.';

const request = createApiRequest({
  prefix: '/users/me/name-change',
  unavailableMessage: 'Name changes are not available right now. Please try again later.',
  networkMessage: 'Could not reach the server. Check your connection and try again.',
  makeError: (message, status, code, body) => new NameChangeError(message, status, code, body),
});

const walletQuery = (wallet: string | null | undefined) =>
  wallet ? `?walletAddress=${encodeURIComponent(wallet)}` : '';

/** The overview; `wallet` (this phone's) adds its spendable MCGP balance to the quote. */
export function getNameChange(wallet?: string | null): Promise<NameChangeOverview> {
  return request<NameChangeOverview>(walletQuery(wallet));
}

/** The message `wallet` signs to prove it is hers, for a fee paid on `chain` ('sonic' for MCGP). */
export function getWalletProof(wallet: string, chain: string): Promise<WalletProof> {
  return request<WalletProof>(`/wallet-proof?walletAddress=${encodeURIComponent(wallet)}&chain=${encodeURIComponent(chain)}`);
}

/**
 * Open a change and start paying. Spendable MCGP is debited by the server
 * (nothing to sign); a stablecoin answers with the transfer to make.
 * `idempotencyKey` is held by the screen across retries of the same payment.
 */
export function startNameChange(pay: NameChangePay, idempotencyKey: string): Promise<NameChangeStart> {
  return request<NameChangeStart>('', {
    method: 'POST',
    body: JSON.stringify(pay),
    headers: { 'Idempotency-Key': idempotencyKey },
  });
}

/**
 * Hand the server the stablecoin fee's hash to prove on-chain. The change comes
 * back paid — or cancelled and refunding when the payment was short or late
 * (its message says so).
 */
export async function confirmNameChangePayment(txHash: string): Promise<NameChange> {
  const { change } = await request<{ change: NameChange }>('/confirm-payment', { method: 'POST', body: JSON.stringify({ txHash }) });
  return change;
}

/** The Didit session for an attempt: the hosted page to open, and where the attempts stand. */
export type NameChangeSession = {
  url: string;
  sessionId: string;
  /** The attempt's session was already open (nothing new was bought). */
  resumed: boolean;
  attempts: number;
  maxAttempts: number;
};

/** A verification session for the next attempt, or the one already open. */
export function startNameChangeVerification(): Promise<NameChangeSession> {
  return request<NameChangeSession>('/start-verification', { method: 'POST' });
}

/** Only before the first verification attempt: the fee comes back to the paying wallet. */
export async function cancelNameChange(): Promise<NameChange> {
  const { change } = await request<{ change: NameChange }>('/cancel', { method: 'POST' });
  return change;
}

/** A change she can do nothing more with. */
export const isTerminalNameChange = (s: NameChangeStatus): boolean =>
  s === 'approved' || s === 'rejected' || s === 'lapsed' || s === 'cancelled';

/**
 * She may cancel — and get the fee back — until her first attempt: a paid
 * change, or a stablecoin one still to be paid. Never an MCGP debit that is
 * still settling (the server refuses it: the hold could be left stranded).
 */
export const canCancelNameChange = (c: Pick<NameChange, 'status' | 'attempts' | 'payMethod'>): boolean =>
  c.attempts === 0 && (c.status === 'paid' || (c.status === 'paying' && c.payMethod === 'stablecoin'));

/** A fee figure from the server ("1.00") as a number; NaN when it isn't one. */
export const parseFeeUsd = (s: string | null | undefined): number => {
  const t = (s ?? '').trim();
  return /^\d+(\.\d+)?$/.test(t) ? Number(t) : NaN;
};
