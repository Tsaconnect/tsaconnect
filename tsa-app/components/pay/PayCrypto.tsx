// components/pay/PayCrypto.tsx
//
// Connects usePayCrypto to the live panel for a merchant that settles in
// crypto — the same picker, estimate and Pay layout as the naira route, with
// the merchant's own token hidden from the picker (it can't be paid through
// the swap yet). usePayLive keeps the hook in step with the checkout, exactly
// as for naira; the panel shows for a payment reopened mid-flight too, so a
// swap the server never heard of is never hidden behind "Waiting…".
import React, { useMemo } from 'react';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { PayLivePanel } from '@/components/pay/PayLivePanel';
import { usePayCrypto } from '@/hooks/usePayCrypto';
import { usePayLive } from '@/hooks/usePayLive';
import { CHAIN_LABELS } from '@/services/paycrestDeposit';
import { getChainByChainId } from '@/constants/chains';
import type { PayCheckout } from '@/services/payApi';
import { payPickFilter } from '@/utils/payPick';

export function PayCrypto({
  checkout, showForm, onWaiting, onActive,
}: {
  checkout: PayCheckout;
  showForm: boolean;
  /** The attempt reached the server-settles stage — refresh the checkout. */
  onWaiting: () => void;
  /** An attempt is moving money (or waiting on her) — keep the form up. */
  onActive: (active: boolean) => void;
}) {
  const pay = usePayCrypto(checkout);
  usePayLive({ pay, checkout, showForm, onWaiting, onActive });
  // Stable across the screen's 5 s re-renders: the picker rebuilds its list whenever `allow` changes.
  const allow = useMemo(
    () => payPickFilter(checkout),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only these fields decide what is hidden.
    [checkout.settlementMode, checkout.settleChainId, checkout.settleTokenAddress],
  );

  const symbol = checkout.settleTokenSymbol;
  const chain = checkout.settleChainId ? getChainByChainId(checkout.settleChainId) : undefined;
  const chainName = chain ? (CHAIN_LABELS[chain.key] ?? chain.name) : undefined;
  const on = chainName ? ` on ${chainName}` : '';
  const receives = symbol
    ? (amountLabel?: string) => `The store receives ${amountLabel ?? symbol}${on}.`
    : undefined;
  const note = symbol ? `Paying with ${symbol}${on} directly is coming soon` : undefined;

  return (
    <>
      <PayLivePanel
        checkout={checkout} pay={pay} showForm={showForm} allow={allow}
        receives={receives} note={note}
      />
      <AuthorizationModal {...pay.authorization} />
    </>
  );
}
