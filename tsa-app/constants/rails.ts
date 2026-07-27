// User-facing names for the three money rails. The acronyms (P2P / PM / IT)
// are internal shorthand that leaked into the UI — every screen the user sees
// spells the rail out and keeps the acronym in parentheses so support and
// existing users can still map the old vocabulary onto the new labels.
//
// Import from here rather than re-typing the strings; the names are product
// copy, not incidental labels, and PM changes them as a set.

export type RailKey = 'p2p' | 'pm' | 'it';

export interface Rail {
  /** Expanded product name, e.g. "Individual Payment". */
  name: string;
  /** Legacy shorthand kept visible for continuity, e.g. "P2P". */
  acronym: string;
  /** One-line explanation of what the rail does. */
  tagline: string;
  /** Copy for depositing (buying crypto) through this rail. */
  depositDesc: string;
  /** Copy for withdrawing (selling crypto / cashing out) through this rail. */
  withdrawDesc: string;
}

export const RAILS: Record<RailKey, Rail> = {
  p2p: {
    name: 'Individual Payment',
    acronym: 'P2P',
    tagline: 'Trade directly with another user',
    depositDesc: 'Buy crypto directly from another user',
    withdrawDesc: 'Sell crypto directly to another user',
  },
  pm: {
    name: 'Payment Merchant',
    acronym: 'PM',
    tagline: 'Corporate P2P — trade with a verified payment merchant',
    depositDesc: 'Buy crypto from a corporate payment merchant',
    withdrawDesc: 'Sell crypto to a corporate payment merchant',
  },
  it: {
    name: 'Instant Transfer',
    acronym: 'IT',
    tagline: 'Fiat & crypto swap — bank rails, no counterparty',
    depositDesc: 'Fiat & crypto swap — bank transfer, credited instantly',
    withdrawDesc: 'Fiat & crypto swap — paid out to your bank account',
  },
};

/** "Individual Payment (P2P)" — the standard label for headings and rows. */
export const railLabel = (key: RailKey): string =>
  `${RAILS[key].name} (${RAILS[key].acronym})`;

// Deep links into each rail. P2P/PM share one screen: ?side is the user's
// intent (buy = browse sell-ads), ?pm=1 switches the feed to merchant ads.
export const RAIL_ROUTES = {
  p2pBuy: '/p2p?side=buy',
  p2pSell: '/p2p?side=sell',
  pmBuy: '/p2p?pm=1&side=buy',
  pmSell: '/p2p?pm=1&side=sell',
  pm: '/p2p?pm=1',
  itBuy: '/wallet/it/buy',
  itSell: '/wallet/it/sell',
  it: '/wallet/it',
} as const;
