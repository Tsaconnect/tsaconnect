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

import type { P2PPayee, PmPayTo } from '@/services/p2p';

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

/** One shape for every "pay this account" card (PM and direct P2P). */
export interface PayToDetails {
  bankLabel: string;
  accountNumber: string;
  accountName: string;
  payerInstruction?: string | null;
}
export type PayToMode = 'pm' | 'direct-payer' | 'direct-payee';

export function payToFromPm(p: PmPayTo): PayToDetails {
  return { bankLabel: p.bank, accountNumber: p.accountNumber, accountName: p.accountName, payerInstruction: p.payerInstruction };
}

export function payToFromPayee(p: P2PPayee): PayToDetails {
  return { bankLabel: p.bankName?.trim() || p.provider, accountNumber: p.accountNumber, accountName: p.accountName };
}

export function payToCopy(mode: PayToMode, payerName?: string): { heading: string; note: string } {
  if (mode === 'direct-payee') {
    return { heading: 'The buyer pays', note: 'This is the account the buyer was shown. Check it for this exact amount before you release.' };
  }
  if (mode === 'direct-payer') {
    return {
      heading: 'Pay exactly',
      note: "Send this exact amount from an account in your own name, then tap “I've paid — attach proof”. The seller releases your crypto once the money arrives.",
    };
  }
  return {
    heading: 'Pay exactly',
    note: payerName
      ? `Pay from a bank account in YOUR name (${payerName}). A name mismatch routes your trade to manual review. We release your crypto automatically once your transfer lands — no further action needed.`
      : 'Pay from a bank account in YOUR name. A name mismatch routes your trade to manual review. We release your crypto automatically once your transfer lands — no further action needed.',
  };
}

/** Proof URLs are user-supplied: only https images are ever loaded or opened. */
export function safeProofUrl(url?: string | null): string | null {
  const u = url?.trim();
  return u && /^https:\/\/\S+$/i.test(u) ? u : null;
}
