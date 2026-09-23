// components/pay/PayNaira.tsx
//
// Connects usePayNaira to the live panel for a merchant that settles in naira.
// Also the place a payment of hers that is already under way is picked back up:
// with no form to show (the screen is 'waiting') it resumes her attempt once,
// so a swap the app was closed in the middle of is followed to its end.
import React, { useEffect, useRef } from 'react';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { PayLivePanel } from '@/components/pay/PayLivePanel';
import { usePayNaira } from '@/hooks/usePayNaira';
import type { PayCheckout } from '@/services/payApi';
import { payPickFilter } from '@/utils/payPick';

export function PayNaira({
  checkout, showForm, onWaiting,
}: {
  checkout: PayCheckout;
  showForm: boolean;
  /** The attempt reached the server-settles stage — refresh the checkout. */
  onWaiting: () => void;
}) {
  const pay = usePayNaira(checkout);

  const { stage, resume } = pay;
  useEffect(() => {
    if (stage === 'waiting') onWaiting();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fire on the stage change only.
  }, [stage]);

  const resumedWith = useRef<unknown>(null);
  useEffect(() => {
    if (showForm || stage !== 'idle' || resumedWith.current === resume) return;
    resumedWith.current = resume; // resume changes once her wallet address is known
    void resume();
  }, [showForm, stage, resume]);

  return (
    <>
      {showForm && <PayLivePanel checkout={checkout} pay={pay} allow={payPickFilter(checkout)} />}
      <AuthorizationModal {...pay.authorization} />
    </>
  );
}
