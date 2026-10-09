// utils/p2pAdOpen.ts — pure decisions for opening a trade from the ad detail
// screen (testable without a render harness). The screen runs the txs through
// services/p2pTx.runP2PTx and maps each outcome here.
import {
  formatTokenAmount,
  parseTokenAmount,
  poolApi,
  hasSufficientAllowance,
  type ApiResponse,
  type P2PTrade,
  type UnsignedTx,
} from '@/services/p2p';
import { feeSplit, formatFeePct } from '@/services/p2pFee';
import { p2pErrorMessage, p2pKycCode } from '@/services/p2pErrors';
import type { RunP2PTxResult } from '@/services/p2pTx';

/** What POST /p2p/ads/:id/trades returns. */
export type OpenPrep = { trade: P2PTrade; unsignedTx: UnsignedTx | null };

export const OPEN_FAILED = 'Could not open trade';

// ErrRateStale (p2p_trade_service.go): "p2p: rate source is stale; cannot quote".
const STALE_RATE = /rate source is stale/i;

/** A refusal the screen routes to promptKycRefusal (utils/kycRefusal). */
export interface KycDecision {
  code: string;
  message?: string;
}

export type OpenFollowUp =
  | { kind: 'none' }
  | ({ kind: 'kyc' } & KycDecision)
  | { kind: 'alert'; title: string; message: string; reload: boolean }
  /** Land on the trade. A lost confirm lands there too: its R4 banner retries
   *  from the stashed hash and the reconciler backfills it. */
  | { kind: 'trade'; tradeId: string; notice?: string };

export function openFollowUp(r: RunP2PTxResult<OpenPrep, P2PTrade>): OpenFollowUp {
  switch (r.status) {
    case 'confirmed':
      return { kind: 'trade', tradeId: r.prepared.trade.id };
    case 'confirm-failed':
      return { kind: 'trade', tradeId: r.prepared.trade.id, notice: 'Trade opened — still confirming the escrow.' };
    case 'reverted':
      return {
        kind: 'alert',
        title: OPEN_FAILED,
        message: 'The network rejected the escrow transaction, so nothing was locked (only the network fee was spent). Try again.',
        reload: true,
      };
    case 'no-tx':
      return {
        kind: 'alert',
        title: OPEN_FAILED,
        message: 'The server did not return a transaction. Nothing was sent — please try again.',
        reload: false,
      };
    case 'refused': {
      const res = r.response;
      // A 503 is also an RPC outage or a closed pool — only a stale feed
      // gets the "reactivates automatically" copy.
      if (res.status === 503 && STALE_RATE.test(res.message ?? '')) {
        return {
          kind: 'alert',
          title: 'Live rate unavailable',
          message: 'The rate feed is temporarily down. Try again in a few seconds — the ad reactivates automatically once the feed recovers.',
          reload: true,
        };
      }
      const code = p2pKycCode(res);
      if (code) return { kind: 'kyc', code, message: res.message };
      // 409: the ad closed or its seller was suspended; 503: the ad may now
      // show as stale or paused — refresh what we show.
      return {
        kind: 'alert',
        title: OPEN_FAILED,
        message: p2pErrorMessage(res),
        reload: res.status === 409 || res.status === 503,
      };
    }
    default:
      // aborted (the gas alert has been shown) or busy (another run owns it).
      return { kind: 'none' };
  }
}

export interface ApproveFollowUp {
  proceed: boolean;
  alert?: { title: string; message: string };
  kyc?: KycDecision;
}

/** The allowance read before the approve. A failed read stops the open with
 *  the mapped error — treating it as "0" would start a needless approve. */
export function allowanceReadFollowUp(
  res: ApiResponse<{ allowance: string }>,
): ApproveFollowUp & { allowance?: string } {
  if (res.success && res.data) return { proceed: true, allowance: res.data.allowance };
  const code = p2pKycCode(res);
  if (code) return { proceed: false, kyc: { code, message: res.message } };
  return { proceed: false, alert: { title: OPEN_FAILED, message: p2pErrorMessage(res) } };
}

/** The approve before a buy-ad's openTradeWithDeposit. It moves no funds, so a
 *  slow one is simply retried by tapping again — the allowance is found. */
export function approveFollowUp(r: RunP2PTxResult<unknown, unknown>): ApproveFollowUp {
  const stop = (message: string, title = 'Approval failed'): ApproveFollowUp =>
    ({ proceed: false, alert: { title, message } });
  switch (r.status) {
    case 'confirmed':
      return { proceed: true };
    case 'refused': {
      // Gas, allowance and approve prepare all 403 an unverified user before
      // anything is signed.
      const code = p2pKycCode(r.response);
      if (code) return { proceed: false, kyc: { code, message: r.response.message } };
      return stop(p2pErrorMessage(r.response));
    }
    case 'reverted':
      return stop('The network rejected the approval, so nothing changed (only the network fee was spent). Try again.');
    case 'confirm-failed':
      return stop(
        "Your approval was sent but hasn't confirmed yet. Wait a moment, then tap Open trade again — you may not need to approve again.",
        'Approval still confirming',
      );
    case 'no-tx':
      return stop('The server did not return an approval transaction. Nothing was sent — please try again.');
    default:
      return { proceed: false };
  }
}

/** The approve's "confirm": the allowance as the API reads it covers `needed`.
 *  Polls because the API's node can lag the receipt we saw; never throws. */
export async function waitForAllowance(
  token: string,
  wallet: string,
  chainId: number,
  needed: string,
  opts: { tries?: number; intervalMs?: number } = {},
): Promise<ApiResponse<undefined>> {
  const tries = opts.tries ?? 10;
  const intervalMs = opts.intervalMs ?? 3000;
  for (let i = 0; i < tries; i++) {
    if (i > 0) await new Promise<void>((resolve) => setTimeout(resolve, intervalMs));
    try {
      const res = await poolApi.allowance(token, wallet, chainId);
      if (res.success && hasSufficientAllowance(res.data?.allowance ?? '0', needed)) return { success: true };
    } catch {
      // A failed read is just another poll.
    }
  }
  return { success: false, message: 'The approval is not confirmed yet.' };
}

const NEUTRAL_RECEIVE_NOTE =
  'A platform fee is deducted from the crypto you receive; the exact amount shows on the trade.';

/** "You receive" on a sell-ad: the buyer receives the crypto, so the fee comes
 *  out of it. `grossText` is the decimal token preview. Unknown rate → the
 *  gross amount and a neutral note; never a guessed number. */
export function sellAdReceivePreview(
  grossText: string,
  decimals: number,
  feeBps: number | null | undefined,
  token: string,
): { amount: string; note: string } {
  const pct = formatFeePct(feeBps);
  let split = null;
  try {
    split = feeSplit(parseTokenAmount(grossText, decimals), feeBps);
  } catch {
    split = null;
  }
  if (!split || !pct) return { amount: grossText, note: NEUTRAL_RECEIVE_NOTE };
  const amount = formatTokenAmount(split.net, decimals, 4);
  if (feeBps === 0) return { amount, note: 'No platform fee on this ad.' };
  return { amount, note: `After the ${pct} platform fee (${formatTokenAmount(split.fee, decimals, 4)} ${token}).` };
}

/** On a buy-ad the ad poster receives the crypto and pays the fee. */
export function buyAdFeeNote(feeBps: number | null | undefined): string {
  const pct = formatFeePct(feeBps);
  return pct
    ? `The buyer pays the ${pct} platform fee out of the crypto they receive.`
    : 'The buyer pays the platform fee out of the crypto they receive.';
}
