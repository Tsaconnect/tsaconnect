// components/pay/PayNaira.tsx
//
// Connects usePayNaira to the live panel for a merchant that settles in naira.
// usePayLive keeps the hook in step with the checkout (follow a payment under
// way, reset when the server frees it, report an attempt that is moving money).
// The panel shows whenever the form is payable OR the hook has something to say:
// a payment reopened mid-flight has no form, but its prompts must still show.
import React from 'react';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { PayLivePanel } from '@/components/pay/PayLivePanel';
import { usePayLive } from '@/hooks/usePayLive';
import { usePayNaira } from '@/hooks/usePayNaira';
import type { PayCheckout } from '@/services/payApi';
import { payPickFilter } from '@/utils/payPick';

export function PayNaira({
  checkout, showForm, onWaiting, onActive,
}: {
  checkout: PayCheckout;
  showForm: boolean;
  /** The attempt reached the server-settles stage — refresh the checkout. */
  onWaiting: () => void;
  /** An attempt is moving money (or waiting on her) — keep the form up. */
  onActive: (active: boolean) => void;
}) {
  const pay = usePayNaira(checkout);
  usePayLive({ pay, checkout, showForm, onWaiting, onActive });

  return (
    <>
      {(showForm || pay.stage !== 'idle' || !!checkout.orderId) && (
        <PayLivePanel checkout={checkout} pay={pay} showForm={showForm} allow={payPickFilter(checkout)} />
      )}
      <AuthorizationModal {...pay.authorization} />
    </>
  );
}
