// utils/fiatRails.ts
//
// The three fiat on/off-ramps offered by the wallet's Fund and Send sheets.
// Kept as data in one pure module so the routes are unit-testable and live in a
// single place rather than being spelled out twice in screens/wallet.tsx.
//
// Careful with `side`: it names the user's intent, and app/p2p/index.tsx
// inverts it to pick which ads to browse. ?side=buy shows SELL ads. Getting it
// backwards is silent — the wrong feed renders perfectly happily.
import type { IconName } from '@/components/ui';

/** Which half of the flow the sheet is in: Fund buys crypto, Send sells it. */
export type FiatDirection = 'buy' | 'sell';

export interface FiatRail {
  key: 'p2p' | 'pm' | 'it';
  title: string;
  desc: string;
  icon: IconName;
  route: string;
}

/**
 * Rails for `direction`, in display order: peer-to-peer first (widest
 * liquidity), then the corporate desks.
 */
export function fiatRails(direction: FiatDirection): FiatRail[] {
  const verb = direction === 'buy' ? 'Buy' : 'Sell';
  const counterparty = direction === 'buy' ? 'from' : 'to';

  return [
    {
      key: 'p2p',
      title: 'P2P',
      desc: `${verb} ${counterparty} other users`,
      icon: 'user',
      route: `/p2p?side=${direction}`,
    },
    {
      key: 'pm',
      title: 'PM (Corporate P2P)',
      desc: `${verb} ${counterparty} verified merchants`,
      icon: 'storefront',
      route: `/p2p?side=${direction}&pm=1`,
    },
    {
      key: 'it',
      title: 'IT (Instant Transfer)',
      desc: `${verb} MCGP at the company rate`,
      icon: 'bolt',
      route: `/wallet/it/${direction}`,
    },
  ];
}
