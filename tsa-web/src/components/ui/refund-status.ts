/** A refund's status, as the API names it. */
export type RefundStatus = 'requested' | 'submitted' | 'confirmed' | 'failed' | 'needs_review' | 'cancelled';

export type Look = { label: string; cls: string };

export const refundMap: Record<RefundStatus, Look> = {
  requested: { label: 'Awaiting approval', cls: 'bg-warning-bg text-warning-strong' },
  submitted: { label: 'Sending', cls: 'bg-info-bg text-info-strong' },
  confirmed: { label: 'Refunded', cls: 'bg-success-bg text-success-strong' },
  failed: { label: 'Failed', cls: 'bg-danger-bg text-danger-strong' },
  needs_review: { label: 'Under review', cls: 'bg-warning-bg text-warning-strong' },
  cancelled: { label: 'Cancelled', cls: 'bg-surface-alt text-text-secondary' },
};

/** A refund status in the merchant's words: "Awaiting approval", "Refunded". */
export const refundStatusLabel = (status: RefundStatus) => (refundMap[status] ?? refundMap.cancelled).label;
