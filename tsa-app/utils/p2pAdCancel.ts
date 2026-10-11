// utils/p2pAdCancel.ts — what My Ads does with a sell-ad cancel's outcome
// (pure, testable without a render harness).
import { txOutcomeAlert, type RunP2PTxResult } from '@/services/p2pTx';

export interface AdCancelFollowUp {
  toast?: string;
  alert?: { title: string; message: string };
  /** The cancel was broadcast but not recorded: show it as sent and don't
   *  offer Cancel again until a later reload still shows the ad open. */
  cancelSent: boolean;
  /** Reload now. Skipped when the cancel was sent, since the list would
   *  still show the ad active and re-offer the cancel straight away. */
  reload: boolean;
}

export function adCancelFollowUp(r: RunP2PTxResult<unknown, unknown>): AdCancelFollowUp {
  switch (r.status) {
    case 'confirmed':
    case 'no-tx':
      return { toast: 'Ad cancelled', cancelSent: false, reload: true };
    case 'confirm-failed':
      return {
        alert: {
          title: 'Cancel sent — still confirming',
          message:
            'Your cancel was sent. The ad will update here once it’s recorded — don’t send it again. ' +
            `If it still shows as open after you refresh in a few minutes, contact support with this hash: ${r.txHash}`,
        },
        cancelSent: true,
        reload: false,
      };
    default: {
      const a = txOutcomeAlert('Cancel', r);
      return { alert: a ?? undefined, cancelSent: false, reload: true };
    }
  }
}
