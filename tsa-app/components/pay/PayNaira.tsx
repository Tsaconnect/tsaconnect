// components/pay/PayNaira.tsx
//
// Connects usePayNaira to the live panel for a merchant that settles in naira.
// usePayLive keeps the hook in step with the checkout (follow a payment under
// way, reset when the server frees it, report an attempt that is moving money).
// The panel shows whenever the form is payable OR the hook has something to say:
// a payment reopened mid-flight has no form, but its prompts must still show.
import React, { useMemo } from 'react';
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
  // Stable across the screen's 5 s re-renders: the picker rebuilds its list whenever `allow` changes.
  const allow = useMemo(
    () => payPickFilter(checkout),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only these fields decide what is hidden.
    [checkout.settlementMode, checkout.settleChainId, checkout.settleTokenAddress],
  );

  return (
    <>
      {(showForm || pay.stage !== 'idle' || !!checkout.orderId) && (
        <PayLivePanel checkout={checkout} pay={pay} showForm={showForm} allow={allow} />
      )}
      <AuthorizationModal {...pay.authorization} />
    </>
  );
}
