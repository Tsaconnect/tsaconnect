// utils/otcListings.ts
//
// Turns the admin's OTC settings into the rows the OTC tab lists.
//
// The backend's OTCSettings is a SINGLETON — one desk, MCGP/USDC — so this
// produces at most one listing today. It is shaped as a list anyway because the
// screen is a catalogue: presale listings from third-party projects are the
// stated next step, and a list of one renders identically to a list of many.
// When a real listings table lands, only this function changes.
//
// Pure and dependency-free so the category/pause rules can be tested without
// mounting the screen.
import type { OtcSettings } from '@/services/walletApi';

export type OtcCategory = 'direct' | 'presale';

export interface OtcListing {
  id: string;
  /** What the user is trading, e.g. MCGP. */
  baseSymbol: string;
  /** What they price it in, e.g. USDC. */
  quoteSymbol: string;
  /** Display name for the Name column, e.g. "MCGP/USDC". */
  pair: string;
  /** Admin-entered logo. May be empty or a dead link — the row falls back. */
  logoUrl: string;
  /** Admin-entered classification, e.g. "RWA". Empty hides the chip. */
  assetClass: string;
  category: OtcCategory;
  buyEnabled: boolean;
  sellEnabled: boolean;
}

/** The one desk the OTC contract backs today. */
const MCGP_USDC = { id: 'mcgp-usdc', baseSymbol: 'MCGP', quoteSymbol: 'USDC' } as const;

function normalizeCategory(c: string | undefined): OtcCategory {
  return c === 'presale' ? 'presale' : 'direct';
}

/**
 * The listings to show. `null` settings means they haven't loaded (or the fetch
 * failed) — the desk still lists, with both directions open, because
 * getOtcSettings() fails OPEN and a blank marketplace would read as an outage.
 */
export function otcListingsFrom(settings: OtcSettings | null): OtcListing[] {
  return [
    {
      ...MCGP_USDC,
      pair: `${MCGP_USDC.baseSymbol}/${MCGP_USDC.quoteSymbol}`,
      logoUrl: settings?.logoUrl ?? '',
      assetClass: settings?.assetClass ?? '',
      category: normalizeCategory(settings?.category),
      // Explicit false only — an unloaded setting leaves the direction open.
      buyEnabled: settings?.buyEnabled !== false,
      sellEnabled: settings?.sellEnabled !== false,
    },
  ];
}

/** Listings placed under a given sub-tab by their admin-set category. */
export function listingsForCategory(
  listings: OtcListing[],
  category: OtcCategory,
): OtcListing[] {
  return listings.filter((l) => l.category === category);
}

/** Whether a direction can be traded on this listing. */
export function canTrade(listing: OtcListing, side: 'buy' | 'sell'): boolean {
  return side === 'buy' ? listing.buyEnabled : listing.sellEnabled;
}
