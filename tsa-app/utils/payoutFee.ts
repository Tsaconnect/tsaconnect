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
