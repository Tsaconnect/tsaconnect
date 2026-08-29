// constants/otc.ts
//
// Copy for the OTC desk's paused states.
//
// The buy-closed notice is product copy from PM, not an incidental string: it
// tells the user what still works (Sell, for anything they already bought OTC)
// and where new trading lives (Spot / Instant Transfer). It is deliberately
// longer than a toast can hold, which is why it renders in a sheet with real
// links rather than as a flash message.
//
// Hardcoded on purpose. The admin's pause switch is a boolean — there is no
// "pause reason" field to drive this from, and inventing one for a single
// message would be more machinery than the message is worth. If the reasons
// ever multiply, add the field to OTCSettings and read it here.

export const OTC_BUY_CLOSED_TITLE = 'Buying is closed on OTC';

/** PM's wording, with capitalisation and spacing tidied. */
export const OTC_BUY_CLOSED_BODY =
  'Purchasing of MCGP is currently closed on the OTC marketplace. Kindly use the ' +
  'Sell button to sell your OTC purchase.\n\n' +
  'For new buy/sell, trade MCGP (spendable) directly from the Spot or Instant ' +
  'Transfer marketplace.';

export const OTC_SELL_CLOSED_TITLE = 'Selling is closed on OTC';

export const OTC_SELL_CLOSED_BODY =
  'Selling of MCGP is currently closed on the OTC marketplace.\n\n' +
  'For new buy/sell, trade MCGP (spendable) directly from the Spot or Instant ' +
  'Transfer marketplace.';

export function otcClosedCopy(side: 'buy' | 'sell'): { title: string; body: string } {
  return side === 'buy'
    ? { title: OTC_BUY_CLOSED_TITLE, body: OTC_BUY_CLOSED_BODY }
    : { title: OTC_SELL_CLOSED_TITLE, body: OTC_SELL_CLOSED_BODY };
}
