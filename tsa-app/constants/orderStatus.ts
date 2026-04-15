// constants/orderStatus.ts
// Shared order status colors and formatting helpers

export const STATUS_COLORS: Record<string, { bg: string; text: string }> = {
  pending_payment: { bg: '#FFF3CD', text: '#856404' },
  escrowed: { bg: '#D1ECF1', text: '#0C5460' },
  shipped: { bg: '#DBEAFE', text: '#1E3A8A' },
  delivered: { bg: '#D4EDDA', text: '#155724' },
  completed: { bg: '#D1FAE5', text: '#065F46' },
  refund_requested: { bg: '#F8D7DA', text: '#721C24' },
  refunded: { bg: '#E2E3E5', text: '#383D41' },
  cancelled: { bg: '#F5F5F5', text: '#6C757D' },
};

export function formatStatus(status: string): string {
  return status.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

export function formatDate(dateStr?: string, includeTime?: boolean): string {
  if (!dateStr) return '\u2014';
  try {
    const d = new Date(dateStr);
    const opts: Intl.DateTimeFormatOptions = {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    };
    if (includeTime) {
      opts.hour = '2-digit';
      opts.minute = '2-digit';
    }
    return d.toLocaleDateString('en-US', opts);
  } catch {
    return dateStr;
  }
}
