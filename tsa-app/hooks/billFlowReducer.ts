// hooks/billFlowReducer.ts
//
// Pure state machine for a utility-bill purchase. No side effects and only
// type-only imports, so it's unit-testable without the API / native modules.
// The useBillFlow hook wires this reducer to the real effects.
//
// Flow: validate (optional) → quote (shows the MCGP price + rate for explicit
// user confirmation — the ₦/MCGP rate IS the fee, so it is never hidden) →
// pay (one API call; the backend debits MCGP operator-signed and vends) →
// poll to a terminal state.

import type { BillOrder, BillQuote } from '@/services/billsApi';

export type BillFlowStatus =
  | 'idle'
  | 'validating'
  | 'validated'
  | 'quoting'
  | 'quoted'
  | 'paying'
  | 'vending'
  | 'delivered'
  | 'refunded'
  | 'failed'
  | 'error';

export interface BillFlowState {
  status: BillFlowStatus;
  customerName?: string;
  quote?: BillQuote;
  order?: BillOrder;
  error?: string;
}

export type BillFlowAction =
  | { type: 'RESET' }
  | { type: 'VALIDATE_START' }
  | { type: 'VALIDATE_OK'; name: string }
  | { type: 'VALIDATE_ERR'; error: string }
  | { type: 'QUOTE_START' }
  | { type: 'QUOTE_OK'; quote: BillQuote }
  | { type: 'PAY_START' }
  | { type: 'ORDER_UPDATE'; order: BillOrder }
  | { type: 'ERROR'; error: string };

export const initialBillFlowState: BillFlowState = { status: 'idle' };

// Map a persisted order status to the flow's status. `vend_failed` means the
// refund is still in flight; it reads as `failed` here and flips to `refunded`
// on a later poll.
export function statusFromOrder(order: BillOrder): BillFlowStatus {
  switch (order.status) {
    case 'delivered':
      return 'delivered';
    case 'refunded':
      return 'refunded';
    case 'vend_failed':
    case 'failed':
      return 'failed';
    case 'vending':
      return 'vending';
    case 'paying':
    case 'paid':
      return 'paying';
    case 'expired':
      return 'error';
    default:
      return 'quoted';
  }
}

export function billFlowReducer(state: BillFlowState, action: BillFlowAction): BillFlowState {
  switch (action.type) {
    case 'RESET':
      return initialBillFlowState;
    case 'VALIDATE_START':
      return { ...state, status: 'validating', error: undefined };
    case 'VALIDATE_OK':
      return { ...state, status: 'validated', customerName: action.name };
    case 'VALIDATE_ERR':
      // Validation failures stay on the form (not a full-screen error).
      return { ...state, status: 'idle', error: action.error };
    case 'QUOTE_START':
      return { ...state, status: 'quoting', error: undefined };
    case 'QUOTE_OK':
      return { ...state, status: 'quoted', quote: action.quote, order: action.quote.order };
    case 'PAY_START':
      return { ...state, status: 'paying', error: undefined };
    case 'ORDER_UPDATE': {
      const status = statusFromOrder(action.order);
      return {
        ...state,
        status,
        order: action.order,
        error:
          status === 'error'
            ? action.order.failureReason || 'Quote expired'
            : status === 'failed'
              ? action.order.failureReason || state.error
              : state.error,
      };
    }
    case 'ERROR':
      return { ...state, status: 'error', error: action.error };
    default:
      return state;
  }
}

export function isTerminal(status: BillFlowStatus): boolean {
  return status === 'delivered' || status === 'refunded' || status === 'failed' || status === 'error';
}
