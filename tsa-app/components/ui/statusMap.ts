// Single source of truth for P2P status presentation — and a TSA Pay payment
// link's (one-time, and reusable), as its merchant sees it, and a verified
// user's name change. Namespace-aware because
// `cancelled`/`resolved` mean different things across trade/ad/dispute. Pure +
// testable; StatusPill resolves `tone` -> theme color/bg.
export type StatusKind = 'trade' | 'ad' | 'dispute' | 'payLink' | 'openLink' | 'nameChange';
export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'escrow' | 'accent' | 'neutral';

export interface StatusVisual {
  label: string;
  tone: StatusTone;
}

const TRADE: Record<string, StatusVisual> = {
  pending_payment: { label: 'Pending payment', tone: 'warning' },
  paid: { label: 'Paid', tone: 'info' },
  releasing: { label: 'Releasing', tone: 'escrow' },
  released: { label: 'Completed', tone: 'success' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
  disputed: { label: 'Disputed', tone: 'danger' },
  resolved: { label: 'Resolved', tone: 'info' },
  // derived UI-only
  expired: { label: 'Expired', tone: 'danger' },
};

const AD: Record<string, StatusVisual> = {
  pending: { label: 'Pending', tone: 'warning' },
  active: { label: 'Active', tone: 'success' },
  paused: { label: 'Paused', tone: 'neutral' },
  exhausted: { label: 'Exhausted', tone: 'neutral' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
  // derived UI-only
  rateStale: { label: 'Rate unavailable', tone: 'warning' },
};

const DISPUTE: Record<string, StatusVisual> = {
  open: { label: 'Open', tone: 'warning' },
  resolved: { label: 'Resolved', tone: 'success' },
  rejected: { label: 'Rejected', tone: 'neutral' },
};

const PAY_LINK: Record<string, StatusVisual> = {
  pending: { label: 'Awaiting payment', tone: 'warning' },
  processing: { label: 'Being paid', tone: 'info' },
  succeeded: { label: 'Paid', tone: 'success' },
  failed: { label: 'Failed', tone: 'danger' },
  expired: { label: 'Expired', tone: 'neutral' },
  // derived UI-only: the server keeps a cancelled link as expired (utils/payLinks payLinkStatus)
  cancelled: { label: 'Cancelled', tone: 'neutral' },
};

// A reusable link is taking payments, paused by its owner, or not taking
// payments for another reason (suspended, live off, nowhere to be paid).
const OPEN_LINK: Record<string, StatusVisual> = {
  active: { label: 'Active', tone: 'success' },
  paused: { label: 'Paused', tone: 'neutral' },
  not_taking: { label: 'Not taking payments', tone: 'warning' },
};

// A name change: paid for, verified again with her ID, then applied or reviewed.
const NAME_CHANGE: Record<string, StatusVisual> = {
  paying: { label: 'Awaiting payment', tone: 'warning' },
  paid: { label: 'Ready to verify', tone: 'info' },
  verifying: { label: 'Verifying', tone: 'info' },
  declined: { label: 'Not verified', tone: 'danger' },
  held_for_review: { label: 'Under review', tone: 'warning' },
  approved: { label: 'Approved', tone: 'success' },
  rejected: { label: 'Rejected', tone: 'danger' },
  lapsed: { label: 'Expired', tone: 'neutral' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
};

export const STATUS_MAP: Record<StatusKind, Record<string, StatusVisual>> = {
  trade: TRADE,
  ad: AD,
  dispute: DISPUTE,
  payLink: PAY_LINK,
  openLink: OPEN_LINK,
  nameChange: NAME_CHANGE,
};

/** Resolve a status string in its namespace; unknown values fall back to a neutral pill. */
export function getStatusVisual(kind: StatusKind, status: string): StatusVisual {
  return (
    STATUS_MAP[kind][status] ?? {
      label: status.replace(/_/g, ' '),
      tone: 'neutral',
    }
  );
}
