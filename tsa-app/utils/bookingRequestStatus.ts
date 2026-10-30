// utils/bookingRequestStatus.ts
// The badge a booking request shows. A request is "converted" the moment the
// buyer starts paying, so a converted one takes its wording from its order:
// only the order knows whether the payment actually went through.
import type { BookingRequestStatus } from '@/services/bookingRequestApi';

export type BookingRequestTone = 'warning' | 'success' | 'danger' | 'info' | 'textMuted';

export interface BookingRequestBadge {
  label: string;
  tone: BookingRequestTone;
}

const STATUS_BADGE: Record<BookingRequestStatus, BookingRequestBadge> = {
  requested: { label: 'Pending', tone: 'warning' },
  approved: { label: 'Approved', tone: 'success' },
  rejected: { label: 'Declined', tone: 'danger' },
  expired: { label: 'Expired', tone: 'textMuted' },
  converted: { label: 'Paid', tone: 'success' },
  cancelled: { label: 'Cancelled', tone: 'textMuted' },
};

const CONVERTED_ORDER_BADGE: Record<string, BookingRequestBadge> = {
  pending_payment: { label: 'Awaiting payment', tone: 'warning' },
  cancelled: { label: 'Order cancelled', tone: 'textMuted' },
  refunded: { label: 'Refunded', tone: 'textMuted' },
};

// orderStatus is absent from an API that predates it; "Paid" is what that
// API's clients always showed.
export function bookingRequestBadge(status: BookingRequestStatus, orderStatus?: string): BookingRequestBadge {
  if (status === 'converted' && orderStatus && CONVERTED_ORDER_BADGE[orderStatus]) {
    return CONVERTED_ORDER_BADGE[orderStatus];
  }
  return STATUS_BADGE[status] ?? { label: status, tone: 'textMuted' };
}
