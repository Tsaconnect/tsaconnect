// A seller who is paid into a bank account has the offramp cost added to the
// order's shipping amount (the escrow pays that to the seller in full and takes
// no fee on it). The server reports it as payoutFee; these are the pieces to
// show it apart from the real shipping.

const big = (v?: string): bigint => {
  try {
    return v ? BigInt(v) : 0n;
  } catch {
    return 0n;
  }
};

/** The shipping alone: the amount with the payout fee taken back out, never below zero. */
export const shippingWithoutPayoutFee = (shippingAmount: string, payoutFee?: string): string => {
  const left = big(shippingAmount) - big(payoutFee);
  return (left > 0n ? left : 0n).toString();
};

export const hasPayoutFee = (payoutFee?: string): boolean => big(payoutFee) > 0n;

/** What a seller reads about their bank payout, by stage. */
export const payoutStageText = (
  p: { stage: string; amountNgn: string; bank: string; accountLast4: string },
): { title: string; body: string } => {
  const to = `${p.bank} ••••${p.accountLast4}`;
  const naira = `₦${Number(p.amountNgn).toLocaleString('en-NG', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  switch (p.stage) {
    case 'waiting_for_confirmation':
      return { title: 'Bank payout', body: `${naira} will be sent to ${to} when the buyer confirms.` };
    case 'sending':
      return { title: 'Bank payout on its way', body: `${naira} is being sent to ${to}. This usually takes a few minutes.` };
    case 'paid':
      return { title: 'Bank payout sent', body: `${naira} was sent to ${to}.` };
    case 'paid_wallet':
      return { title: 'Paid to your wallet', body: 'We could not pay your bank, so the funds were sent to your wallet instead.' };
    case 'review':
      return { title: 'Payout being reviewed', body: 'Our team is checking this payout. Your money is safe and we will update you.' };
    default:
      return { title: 'Bank payout cancelled', body: 'This order ended without a payout.' };
  }
};
