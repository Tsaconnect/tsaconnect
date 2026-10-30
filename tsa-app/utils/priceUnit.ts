// Price units a service listing can be priced in (server: models.PriceUnits).
// on_request is a category option only — on a listing a quote is the
// pricing method, never a unit.
export type PriceUnit =
  | 'fixed'
  | 'per_hour'
  | 'per_day'
  | 'per_night'
  | 'per_month'
  | 'per_session'
  | 'per_seat'
  | 'per_ticket';

const UNIT_NOUN: Record<Exclude<PriceUnit, 'fixed'>, string> = {
  per_hour: 'hour',
  per_day: 'day',
  per_night: 'night',
  per_month: 'month',
  per_session: 'session',
  per_seat: 'seat',
  per_ticket: 'ticket',
};

export const PRICE_UNIT_LABELS: Record<PriceUnit, string> = {
  fixed: 'Fixed price',
  per_hour: 'Per hour',
  per_day: 'Per day',
  per_night: 'Per night',
  per_month: 'Per month',
  per_session: 'Per session',
  per_seat: 'Per seat',
  per_ticket: 'Per ticket',
};

/** What a service listing charges, as the API summarises it (models.ServicePriceSummary). */
export interface ServicePriceSummary {
  serviceType: 'contact' | 'order' | 'booking';
  bookingMode?: 'slot' | 'date_range' | 'quantity';
  bookingType?: 'instant' | 'request';
  priceUnit?: string;
  onRequest?: boolean;
  fromUsd: number;
  chargesPerUnit?: boolean;
}

/** "/ seat" for a per-unit price, "" for a fixed or unset one. */
export function priceUnitSuffix(unit?: string | null): string {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  return noun ? ` / ${noun}` : '';
}

/** What the buyer counts in a quantity booking: "Tickets", "Seats", … */
export function quantityLabel(unit?: string | null): string {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  return noun ? `${noun[0].toUpperCase()}${noun.slice(1)}s` : 'Number of people';
}

/**
 * The capacity field of a seat/ticket listing without time slots: its places
 * are counted per date when the booking form asks for one, else in total.
 */
export function placesCapacityCopy(unit: string | null | undefined, dated: boolean): { label: string; helper: string } {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  const places = noun ? `${noun[0].toUpperCase()}${noun.slice(1)}s` : 'Places';
  return dated
    ? { label: `${places} per date`, helper: 'How many can be booked for the same date. 0 means no limit.' }
    : { label: `${places} available`, helper: 'How many can be booked in total. 0 means no limit.' };
}

/** A count of a quantity booking's units: "1 seat", "3 tickets", "2 people". */
export function quantityCount(unit: string | null | undefined, n: number): string {
  const noun = unit ? UNIT_NOUN[unit as keyof typeof UNIT_NOUN] : undefined;
  if (!noun) return `${n} ${n === 1 ? 'person' : 'people'}`;
  return `${n} ${noun}${n === 1 ? '' : 's'}`;
}

/** The units a merchant may pick, from the category's allowed list. */
export function listingPriceUnits(categoryUnits?: string[] | null): PriceUnit[] {
  const all = Object.keys(PRICE_UNIT_LABELS) as PriceUnit[];
  if (!categoryUnits) return all;
  return all.filter((u) => categoryUnits.includes(u));
}

/** What a stay is charged per: "night" unless the listing prices by day or month. */
export function stayUnitNoun(unit?: string | null): string {
  return unit === 'per_day' ? 'day' : unit === 'per_month' ? 'month' : 'night';
}

/** The unit a per-unit price is read in for the listing's booking mode. */
function perUnitSuffix(s: ServicePriceSummary): string {
  if (!s.chargesPerUnit) return '';
  if (s.bookingMode === 'date_range') return ` / ${stayUnitNoun(s.priceUnit)}`;
  return priceUnitSuffix(s.priceUnit) || ' each';
}

/**
 * A listing's price for a card or detail view: "From $50.00 / night",
 * "Price on request", or null when it is not known.
 */
export function servicePriceLabel(s: ServicePriceSummary | undefined, format: (usd: number) => string): string | null {
  if (!s) return null;
  if (s.onRequest) return 'Price on request';
  if (!(s.fromUsd > 0)) return null;
  // Order menus and per-area fares start at this price; a contact price is the price.
  const from = s.serviceType === 'contact' ? '' : 'From ';
  return `${from}${format(s.fromUsd)}${perUnitSuffix(s)}`;
}

/** The detail screen's call to action for a booking listing. */
export function bookingActionLabel(s: ServicePriceSummary | undefined): string {
  if (s?.bookingType === 'request' || s?.onRequest) return 'Request booking';
  switch (s?.bookingMode) {
    case 'date_range':
      return 'Book your stay';
    case 'quantity':
      return s.priceUnit === 'per_ticket' ? 'Get tickets' : 'Book seats';
    default:
      return 'Book a slot';
  }
}
