// utils/rampRow.ts
//
// What an asset row on the IT hub may offer, given the currency the user
// picked and what the ramp can actually do in it right now. Stablecoins are
// below; MCGP, which has no provider, is mcgpRowActions at the end.
//
// Three separate facts decide this and they fail for different reasons, so
// they produce different messages rather than one flat "unavailable":
//
//   no wallet    the user's problem to fix, and they can fix it
//   no provider  we do not serve this currency at all (USD, GHS)
//   no legs      we serve it, but nobody is quoting either side this minute
//
// Pure so the states can be tested without rendering the hub — the screen
// keeps the routing, which is the only part that needs the router.
import type { RampLegs, RampProvider } from '@/services/itApi';

const CONNECT_WALLET = 'Connect a wallet to trade';

export interface RampRowActions {
  /** Whether to render each action. Never true without a provider. */
  buy: boolean;
  sell: boolean;
  /** Set when neither action is offered; the row shows this instead. */
  unavailable?: string;
}

export function stableRowActions(input: {
  token: string;
  currency: string;
  provider: RampProvider;
  legs: RampLegs;
  hasWallet: boolean;
}): RampRowActions {
  const { token, currency, provider, legs, hasWallet } = input;

  // The wallet comes first: without one there is nothing to deliver into or
  // sell from, whatever the ramp supports.
  if (!hasWallet) return { buy: false, sell: false, unavailable: CONNECT_WALLET };

  if (!provider) return { buy: false, sell: false, unavailable: `Not available in ${currency}` };

  // A provider serves the currency but nothing is quoting either side. That is
  // a real and temporary state — it reads as paused rather than unsupported so
  // the user knows to come back.
  if (!legs.buy && !legs.sell) {
    return { buy: false, sell: false, unavailable: `${token} trading is paused in ${currency}` };
  }

  return { buy: legs.buy, sell: legs.sell };
}

/**
 * "Any other token" — the any-token ramp (buy or sell whatever LiFi can
 * swap). Delivery on a buy lands in her wallet the same way MCGP/stablecoin
 * buys do, so the wallet check comes first, as it does for those. The ramp
 * itself is NGN-only. Unlike the other two rows, sell can drop on its own
 * (`RAMP_CONVERSION_SELL` off server-side) while buy stays live — there is no
 * equivalent buy-side flag yet, so buy is never gated on it.
 */
export function anyTokenRowActions(input: {
  currency: string;
  hasWallet: boolean;
  swappedSellEnabled: boolean;
}): RampRowActions {
  const { currency, hasWallet, swappedSellEnabled } = input;

  if (!hasWallet) return { buy: false, sell: false, unavailable: CONNECT_WALLET };

  if (currency !== 'NGN') {
    return { buy: false, sell: false, unavailable: `Not available in ${currency}` };
  }

  return { buy: true, sell: swappedSellEnabled };
}

/**
 * MCGP has no provider or legs — it trades at the admin rate. The backend
 * refuses MCGP buy and sell for an account with no registered wallet, and the
 * hub asks for a wallet this device can sign for, as the stablecoin rows do.
 * Its buy/sell screens are NGN-only.
 */
export function mcgpRowActions(input: { currency: string; hasWallet: boolean }): RampRowActions {
  const { currency, hasWallet } = input;

  if (!hasWallet) return { buy: false, sell: false, unavailable: CONNECT_WALLET };

  if (currency !== 'NGN') {
    return { buy: false, sell: false, unavailable: `Not available in ${currency}` };
  }

  return { buy: true, sell: true };
}
