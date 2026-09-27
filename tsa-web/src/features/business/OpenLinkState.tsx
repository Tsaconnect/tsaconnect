import { Badge } from '@/components/ui/badge';
import type { OpenLink } from './api';
import { openLinkTaking } from './display';

/** "Taking payments", or "Not taking payments" with why (paused, payouts not set…). */
export function OpenLinkState({ link }: { link: OpenLink }) {
  const { taking, reason } = openLinkTaking(link);
  if (taking) return <Badge tone="live">Taking payments</Badge>;
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <Badge tone="neutral">Not taking payments</Badge>
      <span className="text-caption text-text-secondary">{reason}</span>
    </span>
  );
}
