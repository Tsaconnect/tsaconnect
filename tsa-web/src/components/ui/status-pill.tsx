import { cn } from '@/lib/cn';
import { refundMap, type Look, type RefundStatus } from './refund-status';

export type { RefundStatus };

/** A payment's status, plus `cancelled`: an expired payment link its owner closed. */
export type PayStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired' | 'cancelled';

// The words merchants read, not the API's state names.
const map: Record<PayStatus, Look> = {
  pending: { label: 'Waiting', cls: 'bg-warning-bg text-warning-strong' },
  processing: { label: 'Processing', cls: 'bg-info-bg text-info-strong' },
  succeeded: { label: 'Succeeded', cls: 'bg-success-bg text-success-strong' },
  failed: { label: 'Failed', cls: 'bg-danger-bg text-danger-strong' },
  expired: { label: 'Expired', cls: 'bg-surface-alt text-text-secondary' },
  cancelled: { label: 'Cancelled', cls: 'bg-surface-alt text-text-secondary' },
};

function Pill({ label, cls }: Look) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-caption font-semibold', cls)}>
      <span aria-hidden className="size-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}

/** `labels` renames a state where a page has its own words for it (payment links: "Paid"). */
export function StatusPill({ status, labels }: { status: PayStatus; labels?: Partial<Record<PayStatus, string>> }) {
  const s = map[status] ?? map.expired;
  return <Pill label={labels?.[status] ?? s.label} cls={s.cls} />;
}

export function RefundStatusPill({ status }: { status: RefundStatus }) {
  const s = refundMap[status] ?? refundMap.cancelled;
  return <Pill label={s.label} cls={s.cls} />;
}
