// utils/itIntentView.ts
//
// What the Instant Transfer order screen shows for a stablecoin buy the ramp
// router opened (spec 2026-10-03 §6, §14): the quoted breakdown — display
// rate and one line per provider fee, as stored on the intent — and, once a
// company-wallet delivery settles, the link to its transaction. Pure, so it is
// tested without the screen.
import { txExplorerUrl, type AnyChainKey } from '@/constants/chains';
import { formatAssetUnits, formatMinorAsNaira } from '@/utils/stableBuyQuote';
import type { FeeLine } from '@/components/ramp/FeeLines';

/** One fee line as the API stores it on the intent (services.StableBuyFee). */
export interface ITIntentFee {
  kind: string;
  label: string;
  provider: string;
  /** Token base units, 6 decimals. */
  units: string;
  /** Naira minor units at the display rate. */
  ngnMinor: string;
}

export function intentFeeLines(fees: ITIntentFee[] | null | undefined): FeeLine[] {
  if (!Array.isArray(fees)) return [];
  return fees
    .filter((f) => f && typeof f.label === 'string' && typeof f.units === 'string' && /^\d+$/.test(f.units))
    .map((f) => ({
      label: f.label,
      provider: f.provider || undefined,
      token: formatAssetUnits(f.units),
      ngn: f.ngnMinor ? formatMinorAsNaira(f.ngnMinor) : undefined,
    }));
}

/** The rate the Rate row shows, in minor units: the stored display rate (no
 *  provider fee folded in), else the all-in rate on an intent that has none. */
export function intentDisplayRateMinor(intent: { displayRate?: string; rateKobo?: string }): string | undefined {
  const d = (intent.displayRate ?? '').trim();
  return /^\d+$/.test(d) && !/^0+$/.test(d) ? d : intent.rateKobo;
}

/** "View on explorer" for a company-wallet delivery once it settled. Only the
 *  company wallet's hash is ours; txExplorerUrl refuses anything not a hash. */
export function deliveryExplorerUrl(intent: {
  provider?: string;
  status?: string;
  txHash?: string;
  destChain?: string;
}): string | undefined {
  if (intent.provider !== 'treasury' || intent.status !== 'settled' || !intent.txHash || !intent.destChain) {
    return undefined;
  }
  return txExplorerUrl(intent.txHash, { chainKey: intent.destChain.toLowerCase() as AnyChainKey });
}
