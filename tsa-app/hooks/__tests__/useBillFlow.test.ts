import {
  billFlowReducer,
  initialBillFlowState,
  statusFromOrder,
  isTerminal,
  type BillFlowState,
} from '@/hooks/billFlowReducer';
import type { BillOrder, BillQuote } from '@/services/billsApi';

function order(status: BillOrder['status'], extra: Partial<BillOrder> = {}): BillOrder {
  return {
    id: 'o1',
    category: 'airtime',
    provider: '01',
    ngnAmount: 500,
    mcgpAmount: '18518518518518518519',
    rateKobo: '2700',
    status,
    quoteExpiresAt: '',
    createdAt: '',
    ...extra,
  };
}

const quote: BillQuote = {
  order: order('quoted'),
  mcgpAmount: '18518518518518518519',
  rateKobo: '2700',
  ngnAmount: 500,
  expiresAt: '',
};

describe('statusFromOrder', () => {
  it('maps each order status to a flow status', () => {
    expect(statusFromOrder(order('delivered'))).toBe('delivered');
    expect(statusFromOrder(order('refunded'))).toBe('refunded');
    expect(statusFromOrder(order('vend_failed'))).toBe('failed');
    expect(statusFromOrder(order('failed'))).toBe('failed');
    expect(statusFromOrder(order('vending'))).toBe('vending');
    expect(statusFromOrder(order('paying'))).toBe('paying');
    expect(statusFromOrder(order('paid'))).toBe('paying');
    expect(statusFromOrder(order('quoted'))).toBe('quoted');
    expect(statusFromOrder(order('expired'))).toBe('error');
  });
});

describe('isTerminal', () => {
  it('recognises terminal states', () => {
    expect(isTerminal('delivered')).toBe(true);
    expect(isTerminal('refunded')).toBe(true);
    expect(isTerminal('failed')).toBe(true);
    expect(isTerminal('error')).toBe(true);
    expect(isTerminal('vending')).toBe(false);
    expect(isTerminal('paying')).toBe(false);
    expect(isTerminal('quoted')).toBe(false);
  });
});

describe('billFlowReducer', () => {
  it('walks the happy path to delivered', () => {
    let s: BillFlowState = initialBillFlowState;
    s = billFlowReducer(s, { type: 'VALIDATE_START' });
    expect(s.status).toBe('validating');
    s = billFlowReducer(s, { type: 'VALIDATE_OK', name: 'Jane Doe' });
    expect(s.status).toBe('validated');
    expect(s.customerName).toBe('Jane Doe');
    s = billFlowReducer(s, { type: 'QUOTE_START' });
    expect(s.status).toBe('quoting');
    s = billFlowReducer(s, { type: 'QUOTE_OK', quote });
    expect(s.status).toBe('quoted');
    expect(s.quote).toBe(quote);
    s = billFlowReducer(s, { type: 'PAY_START' });
    expect(s.status).toBe('paying');
    s = billFlowReducer(s, { type: 'ORDER_UPDATE', order: order('vending') });
    expect(s.status).toBe('vending');
    s = billFlowReducer(s, {
      type: 'ORDER_UPDATE',
      order: order('delivered', { deliveryPayload: 'TOKEN-1234' }),
    });
    expect(s.status).toBe('delivered');
    expect(s.order?.deliveryPayload).toBe('TOKEN-1234');
  });

  it('routes a vend failure through refund', () => {
    let s = billFlowReducer(initialBillFlowState, { type: 'ORDER_UPDATE', order: order('vending') });
    expect(s.status).toBe('vending');
    s = billFlowReducer(s, { type: 'ORDER_UPDATE', order: order('vend_failed', { failureReason: 'no supply' }) });
    expect(s.status).toBe('failed');
    expect(s.error).toBe('no supply');
    s = billFlowReducer(s, { type: 'ORDER_UPDATE', order: order('refunded') });
    expect(s.status).toBe('refunded');
  });

  it('surfaces an error message', () => {
    const s = billFlowReducer(initialBillFlowState, { type: 'ERROR', error: 'rate stale' });
    expect(s.status).toBe('error');
    expect(s.error).toBe('rate stale');
  });

  it('resets', () => {
    const s = billFlowReducer({ status: 'delivered' }, { type: 'RESET' });
    expect(s).toEqual(initialBillFlowState);
  });

  it('sets error text when an order expires', () => {
    const s = billFlowReducer(initialBillFlowState, {
      type: 'ORDER_UPDATE',
      order: order('expired', { failureReason: 'Quote expired' }),
    });
    expect(s.status).toBe('error');
    expect(s.error).toBe('Quote expired');
  });

  it('lands on stillProcessing when polling outlives the vend', () => {
    // The real-world case: airtime delivers, but ClubKonnect confirms
    // asynchronously and the backend reconciler settles it minutes later —
    // past the app's polling window. Previously the loop just ended and the
    // screen span forever with no way back to the order.
    let s = billFlowReducer({ status: 'vending' }, { type: 'POLL_EXHAUSTED' });
    expect(s.status).toBe('stillProcessing');
    expect(isTerminal('stillProcessing')).toBe(true);

    // A paying order that never confirmed gets the same treatment.
    s = billFlowReducer({ status: 'paying' }, { type: 'POLL_EXHAUSTED' });
    expect(s.status).toBe('stillProcessing');
  });

  it('does not overwrite a resolved order with stillProcessing', () => {
    for (const status of ['delivered', 'refunded', 'failed', 'error'] as const) {
      expect(billFlowReducer({ status }, { type: 'POLL_EXHAUSTED' }).status).toBe(status);
    }
  });

  it('still resolves normally when the vend confirms in time', () => {
    let s = billFlowReducer({ status: 'vending' }, { type: 'ORDER_UPDATE', order: order('delivered') });
    expect(s.status).toBe('delivered');
    s = billFlowReducer(s, { type: 'POLL_EXHAUSTED' });
    expect(s.status).toBe('delivered');
  });

  it('keeps validation failures on the form', () => {
    const s = billFlowReducer({ status: 'validating' }, { type: 'VALIDATE_ERR', error: 'not found' });
    expect(s.status).toBe('idle');
    expect(s.error).toBe('not found');
  });
});
