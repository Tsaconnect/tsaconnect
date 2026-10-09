// utils/p2pTradeView.ts — pure decisions for the trade cockpit (testable
// without a render harness). The screen stays a thin orchestrator.
import { formatFiat, formatTokenAmount, type P2PTrade } from '@/services/p2p';
import type { RunP2PTxResult } from '@/services/p2pTx';

export const RELEASE_CONFIRM_TITLE = 'Have you checked the money is in your account?';

export function releaseConfirmMessage(
  t: Pick<P2PTrade, 'fiatAmount' | 'fiatCurrency' | 'tokenAmount' | 'token'>,
  decimals: number,
): string {
  return (
    `Open your bank or wallet app and check that ${formatFiat(t.fiatAmount)} ${t.fiatCurrency} from the buyer ` +
    `has arrived. Don't rely on a screenshot or an SMS. Releasing sends ` +
    `${formatTokenAmount(t.tokenAmount, decimals, 4)} ${t.token} to the buyer and can't be undone.`
  );
}

export interface TradeTxFollowUp {
  /** The trade the server recorded, to show before the reload lands. */
  trade?: P2PTrade;
  /** Refetch the server truth (anything may have moved on chain or in the DB). */
  reload: boolean;
  /** Another run of the same action owns the spinner — don't clear it. */
  keepActing: boolean;
}

/** What the screen does with a trade action's outcome (the alert copy is
 *  txOutcomeAlert's job). */
export function tradeTxFollowUp(r: RunP2PTxResult<unknown, P2PTrade>): TradeTxFollowUp {
  switch (r.status) {
    case 'confirmed':
      return { trade: r.data, reload: true, keepActing: false };
    case 'busy':
      return { trade: undefined, reload: false, keepActing: true };
    case 'aborted':
      return { trade: undefined, reload: false, keepActing: false };
    default:
      return { trade: undefined, reload: true, keepActing: false };
  }
}
