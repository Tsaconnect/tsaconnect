import { bookingRequestBadge } from '../bookingRequestStatus';

describe('bookingRequestBadge', () => {
  it('waits for the order before calling a converted request paid', () => {
    expect(bookingRequestBadge('converted', 'pending_payment').label).toBe('Awaiting payment');
    expect(bookingRequestBadge('converted', 'escrowed').label).toBe('Paid');
    expect(bookingRequestBadge('converted', 'completed').label).toBe('Paid');
  });

  it('shows an order that ended without payment', () => {
    expect(bookingRequestBadge('converted', 'cancelled').label).toBe('Order cancelled');
    expect(bookingRequestBadge('converted', 'refunded').label).toBe('Refunded');
  });

  it('keeps the old wording when the API sends no order status', () => {
    expect(bookingRequestBadge('converted').label).toBe('Paid');
  });

  it('ignores the order status on requests that have not converted', () => {
    expect(bookingRequestBadge('approved', 'pending_payment').label).toBe('Approved');
    expect(bookingRequestBadge('requested').label).toBe('Pending');
  });
});
