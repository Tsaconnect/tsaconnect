// Every TSA Pay payment and link query key, in one place, so a read and the
// invalidation after a change can't drift apart. A list key starts [kind, mode],
// so invalidating `list(kind, mode)` refreshes every page and filter of it.
import type { Mode, PaymentFilter } from './api';

export type ListKind = 'payments' | 'links' | 'open-links';

export const queryKeys = {
  list: (kind: ListKind, mode: Mode, filter?: PaymentFilter) => (filter ? [kind, mode, filter] : [kind, mode]) as readonly unknown[],
  payment: (mode: Mode, id: string) => ['payment', mode, id] as const,
  openLink: (mode: Mode, id: string) => ['open-link', mode, id] as const,
};
