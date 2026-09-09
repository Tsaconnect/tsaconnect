// utils/rampRow.ts
//
// What a stablecoin row on the IT hub may offer, given the currency the user
// picked and what the ramp can actually do in it right now.
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
  if (!hasWallet) return { buy: false, sell: false, unavailable: 'Connect a wallet to trade' };

  if (!provider) return { buy: false, sell: false, unavailable: `Not available in ${currency}` };

  // A provider serves the currency but nothing is quoting either side. That is
  // a real and temporary state — it reads as paused rather than unsupported so
  // the user knows to come back.
  if (!legs.buy && !legs.sell) {
    return { buy: false, sell: false, unavailable: `${token} trading is paused in ${currency}` };
  }

  return { buy: legs.buy, sell: legs.sell };
}
