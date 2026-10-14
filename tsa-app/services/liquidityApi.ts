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
  totalLiquidity: string;
  collateral: string;
  outstanding: string;
  /** Collateral covers every unrefilled fill: always "100" by contract rule. */
  coveragePercent: string;
  /** Trailing 30-day spread ÷ liquidity, annualised, percent. */
  apr30d: string;
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
  claimable?: string;
  /** Claimed + claimable, all time. */
  earned?: string;
  sharePercent?: string;
  limits: { min: string; max: string };
  minClaim: string;
  paused: boolean;
  /** The account's wallet is no longer the LP wallet: deposits and gas-free
   *  claims wait for Activate from it; withdraw and signed claims never do. */
  walletChanged: boolean;
  pool?: LiquidityPoolView;
}

export type LiquidityActivityKind = 'deposit' | 'withdrawal' | 'claim' | 'earned';

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
  prepareDeposit: (amount: string) => request<LiquidityTxPlan>('POST', '/liquidity/deposit/prepare', { amount }),
  prepareWithdraw: (amount: string) => request<LiquidityTxPlan>('POST', '/liquidity/withdraw/prepare', { amount }),
  prepareClaim: () => request<LiquidityTxPlan>('POST', '/liquidity/claim/prepare', {}),
  /** Gas-free: the operator claims to the LP's own wallet. */
  claimFor: () => request<{ txHash: string }>('POST', '/liquidity/claim-for', {}),
  poolCard: () => request<PoolCard>('GET', '/p2p/pool-card', undefined, { query: { currency: 'NGN' } }),
};
