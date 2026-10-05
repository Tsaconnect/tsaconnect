// utils/pmPayoutFees.ts
//
// Words for the payment-merchant payout fees GET /p2p/pm/me returns. Under
// central inbound the buyer pays TSA Connect's account and the merchant's
// naira is forwarded to their bank; the merchant pays Flutterwave's costs of
// that (owner decision 2026-10-05) and is shown them before posting an ad.
// The schedule is the one the server deducts, so nothing here computes fees.
import { formatNaira } from '@/utils/stableSellQuote';

export interface PayoutFeeBand {
  /** Upper bound of the band in naira; null for the open top band. */
  upToNgn: string | null;
  feeNgn: string;
}

export interface PayoutFeeSchedule {
  fundingFeeBps: number;
  transferFeeTiers: PayoutFeeBand[];
  minPayoutNgn: string;
}

function describeTransferFee(bands: PayoutFeeBand[]): string {
  if (bands.length === 1) return `a transfer fee of ${formatNaira(bands[0].feeNgn)}`;
  const parts = bands.map((b) =>
    b.upToNgn === null
      ? `or ${formatNaira(b.feeNgn)} above that`
      : `${formatNaira(b.feeNgn)} up to ${formatNaira(b.upToNgn)}`
  );
  return `a transfer fee of ${parts.join(', ')}`;
}

/** One paragraph a merchant can price their ads from. */
export function describePayoutFees(s: PayoutFeeSchedule): string {
  const transfer = describeTransferFee(s.transferFeeTiers);
  const costs =
    s.fundingFeeBps > 0 ? `${s.fundingFeeBps / 100}% of what the buyer pays, plus ${transfer}` : transfer;
  return (
    `Each payout to your bank is reduced by Flutterwave’s costs: ${costs}. ` +
    `A trade that would leave less than ${formatNaira(s.minPayoutNgn)} after fees is not paid out automatically.`
  );
}
