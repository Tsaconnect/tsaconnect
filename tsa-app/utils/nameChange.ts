// utils/nameChange.ts
//
// How a name change's money and dates read on screen — shared by the
// explainer/payment form and the status views.
import { CHAIN_LABELS, fmtTokenAmount } from '@/services/paycrestDeposit';
import { nameChangeChainKey } from '@/services/nameChangePay';
import {
  NAME_CHANGE_CODES, NameChangeError, WALLET_CONFLICT, isWalletConflict, parseFeeUsd, type NameChange,
} from '@/services/nameChangeApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';

/** A chain the server named ('base', or an id), as a person names it. */
export const chainLabel = (chain?: string): string => {
  const key = nameChangeChainKey(chain);
  return (key && CHAIN_LABELS[key]) || chain || '';
};

/** A fee string from the server ("1.00") in dollars; the string itself when it isn't a number. */
export const fmtUsd = (fee: string): string => {
  const n = parseFeeUsd(fee);
  return Number.isNaN(n) ? fee : `$${n.toFixed(2)}`;
};

export const fmtDate = (iso?: string): string =>
  iso ? new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) : '';

/** A whole-token amount string from the server, for display. */
export const fmtAmount = (s: string | undefined): string => fmtTokenAmount(Number(s ?? 0));

export const shortAddress = (a: string): string => (a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a);

/** What she gets back if she cancels now. */
export function refundMessage(c: Pick<NameChange, 'payMethod' | 'payAmount' | 'payToken' | 'payChain' | 'payFrom' | 'status'>): string {
  if (c.payMethod === 'free') return '';
  if (c.status === 'paying') return 'Nothing has been charged yet.';
  const amount = `${fmtAmount(c.payAmount)} ${c.payToken}`;
  if (c.payMethod === 'mcgp_spendable') return `Your ${amount} is returned to your MCGP balance.`;
  const to = c.payFrom ? ` (${shortAddress(c.payFrom)})` : '';
  return `We send ${amount} back to the wallet you paid from${to}, on ${chainLabel(c.payChain)}.`;
}

/**
 * A refusal as a sentence: the server's words, except where this phone must
 * say it — a wallet another account holds (a bare 409), and the limits, whose
 * date is read from the refusal's data rather than from its words.
 */
export function nameChangeErrorMessage(e: unknown, fallback: string): string {
  if (isWalletConflict(e)) return WALLET_CONFLICT;
  const next = e instanceof NameChangeError && e.nextAllowedAt ? fmtDate(e.nextAllowedAt) : '';
  switch (next && errorCode(e)) {
    case NAME_CHANGE_CODES.CANCEL_LIMIT:
      return `You've already cancelled a name change recently, so this one can be cancelled for a refund only from ${next}. You can still verify now.`;
    case NAME_CHANGE_CODES.LIMIT:
      return `You've reached the limit of name changes for now. You can change your name again on ${next}.`;
  }
  return errorMessage(e, fallback);
}
