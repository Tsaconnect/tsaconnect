// Price units a service listing can be priced in (server: models.PriceUnits).
// on_request is a category option only — on a listing a quote is the
// pricing method, never a unit.
export type PriceUnit = 'fixed' | 'per_hour' | 'per_night' | 'per_month' | 'per_session' | 'per_seat';

const UNIT_NOUN: Record<Exclude<PriceUnit, 'fixed'>, string> = {
  per_hour: 'hour',
  per_night: 'night',
  per_month: 'month',
  per_session: 'session',
  per_seat: 'seat',
};

export const PRICE_UNIT_LABELS: Record<PriceUnit, string> = {
  fixed: 'Fixed price',
  per_hour: 'Per hour',
  per_night: 'Per night',
  per_month: 'Per month',
  per_session: 'Per session',
  per_seat: 'Per seat',
};

/** "/ seat" for a per-unit price, "" for a fixed or unset one. */
export function priceUnitSuffix(unit?: string | null): string {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  return noun ? ` / ${noun}` : '';
}

/** What the buyer counts in a quantity booking: "Seats", "Sessions", … */
export function quantityLabel(unit?: string | null): string {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  return noun ? `${noun[0].toUpperCase()}${noun.slice(1)}s` : 'Quantity';
}

/** The units a merchant may pick, from the category's allowed list. */
export function listingPriceUnits(categoryUnits?: string[] | null): PriceUnit[] {
  const all = Object.keys(PRICE_UNIT_LABELS) as PriceUnit[];
  if (!categoryUnits) return all;
  return all.filter((u) => categoryUnits.includes(u));
}
