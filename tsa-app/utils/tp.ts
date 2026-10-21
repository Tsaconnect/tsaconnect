// TradePoints → dollars. 1 TP = $0.01 (PM). The reward ladder is defined on the
// same rate (a level pays threshold/100 USD), so this is one constant, not a
// per-screen guess.
import { formatFiat } from '@/theme/format';

export const USD_PER_TP = 0.01;

export function tpToUsd(tp: number): number {
  return isFinite(tp) ? tp * USD_PER_TP : 0;
}

/**
 * "$12.50" for a TP amount. A real but sub-cent balance reads "<$0.01" rather
 * than a "$0.00" that looks like nothing was earned.
 */
export function formatTpUsd(tp: number): string {
  const usd = tpToUsd(tp);
  if (usd > 0 && usd < 0.01) return '<$0.01';
  return formatFiat(usd, 'USD', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

interface RewardsFeeConfig {
  buyerCashbackPercent?: number;
  buyerTpPerUsd?: number;
}

/**
 * What a buyer earns on a marketplace purchase, from the live fee config, e.g.
 * "0.3% cashback + 7 TP per $100 when you confirm delivery". Undefined when the
 * admin has set both to zero, so the checkout shows nothing rather than "0%".
 */
export function rewardsLine(cfg: RewardsFeeConfig | null | undefined): string | undefined {
  const parts: string[] = [];
  const cashback = Number((cfg?.buyerCashbackPercent ?? 0).toFixed(2));
  if (cashback > 0) parts.push(`${cashback}% cashback`);
  const tpPer100 = Number(((cfg?.buyerTpPerUsd ?? 0) * 100).toFixed(2));
  if (tpPer100 > 0) parts.push(`${tpPer100} TP per $100`);
  return parts.length ? `${parts.join(' + ')} when you confirm delivery` : undefined;
}
