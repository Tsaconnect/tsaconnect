// services/liquidityApi.ts
//
// TSA Liquidity LP reads (API spec §15): GET /liquidity/me and
// /liquidity/me/activity. Both are the caller's own — no id is ever sent.
// Token amounts are decimal strings; never parse them with Number().
import { request } from './p2p';

export type LiquidityStatus = 'none' | 'applied' | 'rejected' | 'approved' | 'active' | 'paused';

export interface LiquidityMe {
  status: LiquidityStatus;
  poolOpen: boolean;
  chain?: { id: number; key: string; name: string };
  token?: { symbol: string; address: string; decimals: number };
  wallet?: string;
  balanceReadable: boolean;
  available?: string;
  locked?: string;
  stake?: string;
  aboveMax?: string;
  headroom?: string;
  limits: { min: string; max: string };
  voucher?: { adId: string; status: string; deadline?: string; remaining: string; renewDue: boolean };
  owedNgn: string;
  paused: boolean;
}

export type LiquidityActivityType = 'deposits' | 'withdrawals' | 'payouts';

export interface LiquidityActivityItem {
  id: string;
  type: 'deposit' | 'withdrawal' | 'payout';
  createdAt: string;
  amount?: string;
  txHash?: string;
  amountNgn?: string;
  status?: string;
  reference?: string;
  bankLast4?: string;
  settledAt?: string;
}

export interface LiquidityActivityPage {
  items: LiquidityActivityItem[];
  nextCursor?: string;
}

export const liquidityApi = {
  me: () => request<LiquidityMe>('GET', '/liquidity/me'),
  activity: (type: LiquidityActivityType, cursor?: string) =>
    request<LiquidityActivityPage>('GET', '/liquidity/me/activity', undefined, { query: { type, cursor } }),
};
