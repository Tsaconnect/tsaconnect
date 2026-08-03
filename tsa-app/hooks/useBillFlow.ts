// hooks/useBillFlow.ts
//
// Drives a single utility-bill purchase through the billFlowReducer state
// machine. Payment is spendable MCGP via the backend's operator debit path, so
// there is nothing to sign client-side: quote → (user confirms the MCGP price)
// → one pay call → poll the order to a terminal state.

import { useCallback, useReducer, useRef } from 'react';
import {
  quoteBill,
  payBillOrder,
  getBillOrder,
  validateCustomer,
  type BillCategory,
  type BillQuoteInput,
} from '@/services/billsApi';
import {
  billFlowReducer,
  initialBillFlowState,
  statusFromOrder,
  isTerminal,
} from '@/hooks/billFlowReducer';

export * from '@/hooks/billFlowReducer';

const POLL_INTERVAL_MS = 3000;
// ClubKonnect can retry a vend for up to an hour; the app polls ~90s and then
// leaves the order in `vending` — history shows the final outcome.
const POLL_MAX_ATTEMPTS = 30;

const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function useBillFlow() {
  const [state, dispatch] = useReducer(billFlowReducer, initialBillFlowState);
  // Guards a stale poll loop after a reset/new purchase.
  const runRef = useRef(0);

  const reset = useCallback(() => {
    runRef.current++;
    dispatch({ type: 'RESET' });
  }, []);

  // Validate/resolve the customer before payment. Returns the resolved name (or
  // '' when the category only needs a format check), or null on failure.
  const validate = useCallback(
    async (category: BillCategory, provider: string, customerId: string): Promise<string | null> => {
      dispatch({ type: 'VALIDATE_START' });
      const r = await validateCustomer({ category, provider, customerId });
      if (!r.success) {
        dispatch({ type: 'VALIDATE_ERR', error: r.message || 'Validation failed' });
        return null;
      }
      const name = r.data?.customerName || '';
      dispatch({ type: 'VALIDATE_OK', name });
      return name;
    },
    []
  );

  // Stage a quote. The screen shows the MCGP amount + ₦/MCGP rate from the
  // result and asks the user to confirm before pay() is called.
  const quote = useCallback(async (input: BillQuoteInput): Promise<boolean> => {
    dispatch({ type: 'QUOTE_START' });
    const q = await quoteBill(input);
    if (!q.success || !q.data) {
      dispatch({ type: 'ERROR', error: q.message || 'Could not price this bill' });
      return false;
    }
    dispatch({ type: 'QUOTE_OK', quote: q.data });
    return true;
  }, []);

  // Pay the staged quote and poll to a terminal status.
  const pay = useCallback(
    async (orderId: string): Promise<void> => {
      const run = ++runRef.current;
      dispatch({ type: 'PAY_START' });
      const p = await payBillOrder(orderId);
      if (!p.success || !p.data) {
        dispatch({ type: 'ERROR', error: p.message || 'Payment failed' });
        return;
      }
      dispatch({ type: 'ORDER_UPDATE', order: p.data });

      let order = p.data;
      for (let i = 0; i < POLL_MAX_ATTEMPTS && !isTerminal(statusFromOrder(order)); i++) {
        await delay(POLL_INTERVAL_MS);
        if (runRef.current !== run) return; // reset/new purchase started
        const g = await getBillOrder(order.id);
        if (g.success && g.data) {
          order = g.data;
          dispatch({ type: 'ORDER_UPDATE', order });
        }
      }
    },
    []
  );

  return { state, validate, quote, pay, reset };
}
