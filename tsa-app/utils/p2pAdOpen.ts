// utils/p2pAdOpen.ts — pure decisions for opening a trade from the ad detail
// screen (testable without a render harness). The screen runs the txs through
// services/p2pTx.runP2PTx and maps each outcome here.
import {
  formatTokenAmount,
  parseTokenAmount,
  poolApi,
  hasSufficientAllowance,
  tradesApi,
  type ApiResponse,
  type P2PAd,
  type P2PTrade,
  type UnsignedTx,
} from '@/services/p2p';
import { feeSplit, formatFeePct } from '@/services/p2pFee';
import { p2pCaughtMessage, p2pErrorMessage, p2pKycCode } from '@/services/p2pErrors';
import { P2P_TX_COPY, p2pTxFollowUp, type P2PFollowUp, type RunP2PTxResult } from '@/services/p2pTx';
import { OPEN_ABANDONED_NEXT } from '@/utils/p2pTradeView';

/** What POST /p2p/ads/:id/trades returns. */
export type OpenPrep = { trade: P2PTrade; unsignedTx: UnsignedTx | null };

const OPEN_FAILED = 'Could not open trade';

// ErrRateStale (p2p_trade_service.go): "p2p: rate source is stale; cannot quote".
const STALE_RATE = /rate source is stale/i;

// ErrActiveTradeExists (p2p_trade_service.go): "p2p: you already have an
// active trade on this ad". The server inserts the trade at prepare, so a
// reverted or abandoned open still holds this slot until it expires.
const ACTIVE_TRADE_EXISTS = /already have an active trade on this ad/i;

export const ACTIVE_TRADE_TITLE = 'You already have a trade on this ad';
export const ACTIVE_TRADE_WITH_LINK = 'Finish it, or wait for it to expire, before opening another.';
export const ACTIVE_TRADE_NO_LINK = 'You already have a trade on this ad — find it in My trades.';

function openOutcome(r: RunP2PTxResult<OpenPrep, P2PTrade>): P2PFollowUp {
  return p2pTxFollowUp(r, {
    label: 'Open trade',
    confirmed: (c) => ({ navigate: c.prepared.trade.id, reload: false }),
    // Land on the trade: its R4 banner retries the stashed hash and the
    // reconciler backfills it.
    'confirm-failed': (c) => ({
      toast: { type: 'info', message: 'Trade opened — still confirming the escrow.' },
      navigate: c.prepared.trade.id,
      reload: false,
    }),
    // The trade row stays until the watchdog expires it, so "try again" on
    // this ad would be refused as an active trade.
    reverted: (c) => ({
      alert: {
        title: OPEN_FAILED,
        message: P2P_TX_COPY.revertedMessage('the escrow transaction', OPEN_ABANDONED_NEXT, 'nothing was locked'),
        viewTradeId: c.prepared.trade.id,
      },
    }),
    'no-tx': { alert: { title: OPEN_FAILED, message: P2P_TX_COPY.noTxMessage() }, reload: false },
    refused: ({ response: res }) => {
      if (p2pKycCode(res)) return {};
      // A 503 is also an RPC outage or a closed pool — only a stale feed
      // gets the "reactivates automatically" copy.
      if (res.status === 503 && STALE_RATE.test(res.message ?? '')) {
        return {
          alert: {
            title: 'Live rate unavailable',
            message: 'The rate feed is temporarily down. Try again in a few seconds — the ad reactivates automatically once the feed recovers.',
          },
          reload: true,
        };
      }
      // 409: the ad closed or its seller was suspended; 503: the ad may now
      // show as stale or paused — refresh what we show.
      return {
        alert: { title: OPEN_FAILED, message: p2pErrorMessage(res) },
        reload: res.status === 409 || res.status === 503,
      };
    },
  });
}

/** Every runP2PTx outcome of an open. A refusal because this user already
 *  holds a trade on the ad looks that trade up to link it. */
export async function openFollowUp(
  r: RunP2PTxResult<OpenPrep, P2PTrade>,
  ad: Pick<P2PAd, 'id' | 'side'>,
): Promise<P2PFollowUp> {
  if (r.status === 'refused' && !p2pKycCode(r.response) && ACTIVE_TRADE_EXISTS.test(r.response.message ?? '')) {
    const tradeId = await findMyActiveTradeOnAd(ad);
    return {
      alert: tradeId
        ? { title: ACTIVE_TRADE_TITLE, message: ACTIVE_TRADE_WITH_LINK, viewTradeId: tradeId, dismissText: 'Close' }
        : { title: ACTIVE_TRADE_TITLE, message: ACTIVE_TRADE_NO_LINK },
      reload: false,
    };
  }
  return openOutcome(r);
}

/** A sign or broadcast failure (runP2PTx threw): nothing reached the chain.
 *  When the prepare had already succeeded the server holds a trade row for
 *  this attempt until it expires, so say that instead of inviting a retry
 *  that would be refused. */
export function openThrownFollowUp(err: unknown, preparedTradeId?: string): P2PFollowUp {
  const reason = p2pCaughtMessage(err);
  if (!preparedTradeId) return { alert: { title: OPEN_FAILED, message: reason }, reload: false };
  return {
    alert: { title: OPEN_FAILED, message: `${reason}\n\nNothing was locked. ${OPEN_ABANDONED_NEXT}`, viewTradeId: preparedTradeId },
    reload: true,
  };
}

const LIVE_TRADE_STATUSES: readonly P2PTrade['status'][] = ['pending_payment', 'paid', 'releasing', 'disputed'];

/** The caller's live trade on `ad`, to link from the active-trade refusal.
 *  The opener is the buyer on a sell-ad and the seller on a buy-ad. Best
 *  effort: null when it can't be found; never throws. */
export async function findMyActiveTradeOnAd(ad: Pick<P2PAd, 'id' | 'side'>): Promise<string | null> {
  try {
    const res = await tradesApi.list({ role: ad.side === 'buy' ? 'seller' : 'buyer', limit: 50 });
    if (!res.success || !res.data) return null;
    const hit = res.data.find((t) => t.adId === ad.id && LIVE_TRADE_STATUSES.includes(t.status));
    return hit?.id ?? null;
  } catch {
    return null;
  }
}

/** The allowance read before the approve. A failed read stops the open with
 *  the mapped error — treating it as "0" would start a needless approve. */
export function allowanceReadFollowUp(
  res: ApiResponse<{ allowance: string }>,
): P2PFollowUp & { allowance?: string } {
  if (res.success && res.data) return { proceed: true, allowance: res.data.allowance, reload: false };
  const code = p2pKycCode(res);
  if (code) return { proceed: false, kyc: { code, message: res.message }, reload: false };
  return { proceed: false, alert: { title: OPEN_FAILED, message: p2pErrorMessage(res) }, reload: false };
}

/** The approve before a buy-ad's openTradeWithDeposit. It moves no funds, so a
 *  slow one is simply retried by tapping again — the allowance is found. */
export function approveFollowUp(r: RunP2PTxResult<unknown, unknown>): P2PFollowUp {
  const next = p2pTxFollowUp(r, {
    // Gas, allowance and approve prepare all 403 an unverified user before
    // anything is signed: the default refusal routes that to the KYC prompt.
    label: 'Approval',
    confirmed: { proceed: true },
    reverted: { alert: { title: 'Approval failed', message: P2P_TX_COPY.revertedMessage('the approval', 'Try again.') } },
    'confirm-failed': {
      alert: {
        title: 'Approval still confirming',
        message: "Your approval was sent but hasn't confirmed yet. Wait a moment, then tap Open trade again — you may not need to approve again.",
      },
    },
    'no-tx': { alert: { title: 'Approval failed', message: P2P_TX_COPY.noTxMessage('an approval transaction') } },
  });
  return { ...next, proceed: next.proceed ?? false, reload: false };
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
  'A fee is deducted from the crypto you receive; the exact amount shows on the trade.';

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
  if (feeBps === 0) return { amount, note: 'No fee on this ad.' };
  return { amount, note: `After the ${pct} fee (${formatTokenAmount(split.fee, decimals, 4)} ${token}).` };
}

/** On a buy-ad the ad poster receives the crypto and pays the fee. */
export function buyAdFeeNote(feeBps: number | null | undefined): string {
  const pct = formatFeePct(feeBps);
  return pct
    ? `The buyer pays the ${pct} fee out of the crypto they receive.`
    : 'The buyer pays the fee out of the crypto they receive.';
}
