// utils/payInto.ts
//
// Reading the pay-into cards the API returns for naira buys (MCGP, stablecoin,
// P2P payment-merchant trades).
//
// Under INBOUND_MODE=central every payer is sent to one company account and the
// transfer is attributed by the sender's bank-account name plus the exact
// amount. The API marks that mode by attaching `payerInstruction` (and, for
// MCGP buys, `amountNgn`) to the card. Neither is present in virtual-account
// mode or when a P2P card points at a merchant's own account — screens must key
// off their presence rather than guess the mode.

export interface CentralPayToFields {
  /** Server-authored notice: which account holder to pay from (the user's
   *  verified name when known), and why the recipient name is not "TSA Connect".
   *  Present only in central mode. */
  payerInstruction?: string | null;
  /** Whole naira the matcher compares against. Present only in central mode. */
  amountNgn?: string | null;
}

/** The instruction to render, or null when this card needs none. */
export function payerInstructionOf(payTo?: CentralPayToFields | null): string | null {
  const text = payTo?.payerInstruction?.trim();
  return text ? text : null;
}

/** The exact whole-naira figure to transfer, grouped for display ("12,500"),
 *  or null when the card carries none. Anything but plain digits is refused
 *  rather than reformatted: a wrong figure here is a transfer nothing matches. */
export function exactNairaOf(payTo?: CentralPayToFields | null): string | null {
  const raw = payTo?.amountNgn?.trim();
  if (!raw || !/^\d+$/.test(raw) || /^0+$/.test(raw)) return null;
  return raw.replace(/^0+/, '').replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}
