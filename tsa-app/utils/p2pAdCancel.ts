// utils/p2pAdCancel.ts — what My Ads does with an ad cancel's outcome (pure,
// testable without a render harness; applied by utils/p2pFollowUp).
import { P2P_TX_COPY, p2pTxFollowUp, type P2PFollowUp, type RunP2PTxResult } from '@/services/p2pTx';

export function adCancelFollowUp(r: RunP2PTxResult<unknown, unknown>): P2PFollowUp {
  const cancelled: Partial<P2PFollowUp> = { toast: { type: 'success', message: 'Ad cancelled' } };
  return p2pTxFollowUp(r, {
    label: 'Cancel',
    confirmed: cancelled,
    // A buy-ad cancel is DB-only: the server finished it.
    'no-tx': cancelled,
    // Reload now is skipped: the list would still show the ad active and
    // re-offer the cancel straight away.
    'confirm-failed': (c) => ({
      alert: {
        title: P2P_TX_COPY.stillConfirmingTitle('Cancel'),
        message:
          'Your cancel was sent. The ad will update here once it’s recorded — don’t send it again. ' +
          `If it still shows as open after you refresh in a few minutes, contact support with this hash: ${c.txHash}`,
      },
      cancelSent: true,
      reload: false,
    }),
    aborted: { reload: true },
    busy: { reload: true },
  });
}
