// components/pay/PayCrypto.tsx
//
// Connects usePayCrypto to the live panel for a merchant that settles in
// crypto — the same picker, estimate and Pay layout as the naira route, with
// the merchant's own token hidden from the picker (it can't be paid through
// the swap yet). With no form to show (the screen is 'waiting') it resumes her
// attempt once, so a swap the app was closed in the middle of still reaches the
// server.
import React, { useEffect, useRef } from 'react';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { PayLivePanel } from '@/components/pay/PayLivePanel';
import { usePayCrypto } from '@/hooks/usePayCrypto';
import { getChainByChainId } from '@/constants/chains';
import type { PayCheckout } from '@/services/payApi';
import { payPickFilter } from '@/utils/payPick';

export function PayCrypto({
  checkout, showForm, onWaiting,
}: {
  checkout: PayCheckout;
  showForm: boolean;
  /** The attempt reached the server-settles stage — refresh the checkout. */
  onWaiting: () => void;
}) {
  const pay = usePayCrypto(checkout);

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

  const symbol = checkout.settleTokenSymbol;
  const chain = checkout.settleChainId ? getChainByChainId(checkout.settleChainId)?.name : undefined;
  const receives = symbol ? `The store receives ${symbol}${chain ? ` on ${chain}` : ''}.` : undefined;
  const note = symbol
    ? `Paying with ${symbol}${chain ? ` on ${chain}` : ''} directly is coming soon`
    : undefined;

  return (
    <>
      {showForm && (
        <PayLivePanel checkout={checkout} pay={pay} allow={payPickFilter(checkout)} receives={receives} note={note} />
      )}
      <AuthorizationModal {...pay.authorization} />
    </>
  );
}
