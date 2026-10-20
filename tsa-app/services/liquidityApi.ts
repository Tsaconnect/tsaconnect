// services/liquidityApi.ts
//
// TSA Liquidity, shared pool (spec 2026-10-14): the caller's own LP position,
// activation, and the unsigned txs the LP signs. Every call is the caller's
// own — no id is ever sent. Token amounts are decimal strings in whole
// tokens; never parse them with Number().
import { request, type UnsignedTx } from './p2p';
import { withWalletProof } from './walletApi';

export type LiquidityStatus = 'none' | 'active' | 'paused';

export interface LiquidityPoolView {
  totalLiquidity?: string;
  /** Trailing 30-day spread ÷ liquidity, annualised, percent. */
  apr30d?: string;
}

/** One exit request of a naira LP. Principal is naira; earnings are USDT. */
export interface FiatWithdrawal {
  id: string;
  kind: 'principal' | 'earnings';
  status: 'requested' | 'paid' | 'rejected';
  fiat?: string;
  units?: string;
  paidUnits?: string;
  txHash?: string;
  requestedAt: string;
  resolvedAt?: string;
}

export interface FiatDeposit {
  id: string;
  txHash: string;
  units: string;
  rate: string;
  fiat: string;
  createdAt: string;
}

/** A naira LP: USDT sent to the company desk, locked in naira at that day's rate. */
export interface FiatPosition {
  /** May add USDT (verified, not the company account). Exits are never gated on it. */
  eligible: boolean;
  /** The account's proven wallet: the only one the USDT may be sent from. */
  wallet?: string;
  currency: 'NGN';
  /** Where the USDT is sent. */
  desk: string;
  chain?: { id: number; key: string };
  token?: { symbol: string; address: string; decimals: number };
  /** Naira owed; the USDT behind it; the average rate it was locked at. */
  principal: string;
  units: string;
  avgRate?: string;
  currentRate?: string;
  /** What an exit would pay today, in USDT. */
  todayUsdt?: string;
  earned: string;
  earningsAvailable: string;
  earningsNaira?: string;
  minDeposit: string;
  minEarnings: string;
  deposits: FiatDeposit[];
  withdrawals: FiatWithdrawal[];
}

export interface LiquidityMe {
  mode: 'vault';
  status: LiquidityStatus;
  poolOpen: boolean;
  chain?: { id: number; key: string; name: string };
  token?: { symbol: string; address: string; decimals: number };
  vault: string;
  wallet?: string;
  /** The wallet may deposit (allow-listed on the vault). */
  allowed: boolean;
  balanceReadable: boolean;
  principal?: string;
  /** Principal that may be withdrawn now: a deposit is locked for 24 hours. */
  withdrawable?: string;
  /** When the rest of the principal unlocks; absent when nothing is locked. */
  lockedUntil?: string;
  /** Withdrawn principal not yet paid out; omitted when zero. */
  pendingWithdrawal?: string;
  claimable?: string;
  /** Claimed + claimable, all time. */
  earned?: string;
  sharePercent?: string;
  limits: { min: string; max: string };
  /** False when the API could not read the pool's limits: min/max are empty and deposits are refused. */
  limitsReadable?: boolean;
  minClaim: string;
  paused: boolean;
  /** The account's wallet is no longer the LP wallet: deposits and gas-free
   *  claims wait for Activate from it; withdraw and signed claims never do. */
  walletChanged: boolean;
  pool?: LiquidityPoolView;
}

export type LiquidityActivityKind =
  | 'deposit' | 'withdrawal' | 'claim' | 'earned' | 'withdrawal_pending' | 'withdrawal_paid';

export interface LiquidityActivityItem {
  kind: LiquidityActivityKind;
  amount: string;
  to?: string;
  txHash: string;
  at: string;
}

export interface LiquidityActivityPage {
  items: LiquidityActivityItem[];
  nextCursor?: string;
}

/** What the LP signs next. Step "approve": the deposit is prepared again once it mines. */
export interface LiquidityTxPlan {
  step: 'approve' | 'deposit' | 'withdraw' | 'claim';
  txs: UnsignedTx[];
}

/** The pinned "TSA Connect · Recommended" card. enabled=false: dormant, show nothing. */
export interface PoolCard {
  enabled: boolean;
  name?: string;
  badge?: string;
  available: boolean;
  rate?: string;
  fundingFeePercent?: string;
  minNgn?: string;
  maxNgn?: string;
  token?: { symbol: string; address: string; decimals: number };
  chain?: { id: number; key: string; name: string };
}

export const liquidityApi = {
  me: () => request<LiquidityMe>('GET', '/liquidity/me'),
  activity: (cursor?: string) =>
    request<LiquidityActivityPage>('GET', '/liquidity/me/activity', undefined, { query: { cursor } }),
  /** Instant, on the proven wallet: proves it first when the API asks. */
  activate: () => withWalletProof(() => request<LiquidityMe>('POST', '/liquidity/activate', {})),
  fiat: () => request<FiatPosition>('GET', '/liquidity/fiat'),
  /** After the USDT transfer to the desk is sent: the API proves it on-chain and locks the rate. */
  fiatDeposit: (txHash: string) => request<FiatDeposit>('POST', '/liquidity/fiat/deposits', { txHash }),
  /** principal: amount in naira; earnings: amount in USDT. An admin pays it. */
  fiatWithdraw: (kind: 'principal' | 'earnings', amount: string) =>
    request<FiatWithdrawal>('POST', '/liquidity/fiat/withdrawals', { kind, amount }),
  prepareDeposit: (amount: string) => request<LiquidityTxPlan>('POST', '/liquidity/deposit/prepare', { amount }),
  prepareWithdraw: (amount: string) => request<LiquidityTxPlan>('POST', '/liquidity/withdraw/prepare', { amount }),
  prepareClaim: () => request<LiquidityTxPlan>('POST', '/liquidity/claim/prepare', {}),
  /** Gas-free: the operator claims to the LP's own wallet. */
  claimFor: () => request<{ txHash: string }>('POST', '/liquidity/claim-for', {}),
  poolCard: () => request<PoolCard>('GET', '/p2p/pool-card', undefined, { query: { currency: 'NGN' } }),
};
