import { cn } from '@/lib/cn';

export type PayStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired';

// The words merchants read, not the API's state names.
const map: Record<PayStatus, { label: string; cls: string }> = {
  pending: { label: 'Waiting', cls: 'bg-warning-bg text-warning-strong' },
  processing: { label: 'Processing', cls: 'bg-info-bg text-info-strong' },
  succeeded: { label: 'Succeeded', cls: 'bg-success-bg text-success-strong' },
  failed: { label: 'Failed', cls: 'bg-danger-bg text-danger-strong' },
  expired: { label: 'Expired', cls: 'bg-surface-alt text-text-secondary' },
};

export function StatusPill({ status }: { status: PayStatus }) {
  const s = map[status] ?? map.expired;
  return (
    <span className={cn('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-caption font-semibold', s.cls)}>
      <span aria-hidden className="size-1.5 rounded-full bg-current" />
      {s.label}
    </span>
  );
}
