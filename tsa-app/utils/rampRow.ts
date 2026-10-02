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
//   no legs      we serve it, but nobody is quoting that side this minute —
//                its button stays, and a tap says to check again later
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
  /** A side that is closed for now (nobody is quoting it) keeps its button,
   *  and a tap shows this message instead of opening a screen that would only
   *  fail. A button that simply vanished read as a bug (owner, 2026-10-02). */
  closed?: { buy?: string; sell?: string };
}

const CHECK_LATER = "isn't available right now. Please check again later.";

/** The "check again later" message for a side nobody is quoting. */
const closedNote = (side: 'buy' | 'sell', what: string, currency: string) =>
  side === 'buy' ? `Buying ${what} with ${currency} ${CHECK_LATER}` : `Selling ${what} for ${currency} ${CHECK_LATER}`;

/** The live flags, plus the given message for each closed side. */
function withClosed(buy: boolean, sell: boolean, notes: { buy: string; sell: string }): RampRowActions {
  const closed: { buy?: string; sell?: string } = {};
  if (!buy) closed.buy = notes.buy;
  if (!sell) closed.sell = notes.sell;
  return closed.buy || closed.sell ? { buy, sell, closed } : { buy, sell };
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

  // A provider serves the currency, so a side nobody is quoting is a real and
  // temporary state: its button stays and says to come back, rather than
  // reading as unsupported.
  return withClosed(legs.buy, legs.sell, {
    buy: closedNote('buy', token, currency),
    sell: closedNote('sell', token, currency),
  });
}

/**
 * "Any other token" — the any-token ramp (buy or sell whatever LiFi can
 * swap). Delivery on a buy lands in her wallet the same way MCGP/stablecoin
 * buys do, so the wallet check comes first, as it does for those. The ramp
 * itself is NGN-only. Both sides run through the same Paycrest legs as the
 * stablecoin rows (a buy is createBuy on paycrest/buy), so they follow them;
 * sell can also drop on its own (`RAMP_CONVERSION_SELL` off server-side).
 *
 * `legs` is null when they are not known yet (the currency list is loading,
 * or failed to load). Unknown is read as open, as the server reads an
 * unreadable order book: an over-reported side costs one refused quote, while
 * an under-reported one turns the whole row away.
 */
export function anyTokenRowActions(input: {
  currency: string;
  hasWallet: boolean;
  swappedSellEnabled: boolean;
  legs: RampLegs | null;
}): RampRowActions {
  const { currency, hasWallet, swappedSellEnabled, legs } = input;

  if (!hasWallet) return { buy: false, sell: false, unavailable: CONNECT_WALLET };

  if (currency !== 'NGN') {
    return { buy: false, sell: false, unavailable: `Not available in ${currency}` };
  }

  const open = legs ?? { buy: true, sell: true };
  return withClosed(open.buy, open.sell && swappedSellEnabled, {
    buy: closedNote('buy', 'other tokens', currency),
    // A switched-off feature is not a gap in liquidity, so no "check again later".
    sell: swappedSellEnabled
      ? closedNote('sell', 'other tokens', currency)
      : "Selling other tokens isn't available at the moment.",
  });
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
