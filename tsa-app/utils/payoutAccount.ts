// utils/payoutAccount.ts
//
// Per-currency rules for a payout/refund account number field (bank transfer
// destinations on the Paycrest buy/sell screens).
//
// NUBAN (Nigeria) is a fixed 10 digits, and it's the only rule this codebase
// actually knows — every other currency's account-number format varies by
// country and bank. So an unknown currency validates as "non-empty" rather
// than being forced through NGN's 10-digit rule: that rule would silently
// reject a real GHS/EUR/KES account number that isn't 10 digits, which is
// worse than accepting one we can't independently verify. The lookup only
// grows when a fixed rule is actually confirmed for a currency — never guess.
const FIXED_DIGIT_LENGTHS: Record<string, number> = {
  NGN: 10, // NUBAN
};

/** A generous cap for currencies with no known fixed length — long enough for
 *  an IBAN (34 chars, the longest in use) without being unbounded. */
const DEFAULT_MAX_LENGTH = 34;

function fixedLengthFor(currency: string): number | undefined {
  return FIXED_DIGIT_LENGTHS[currency.toUpperCase()];
}

/** Max length to cap a text input at while the user types. */
export function accountNumberMaxLength(currency: string): number {
  return fixedLengthFor(currency) ?? DEFAULT_MAX_LENGTH;
}

/** Whether this currency's account numbers are known to be digits-only
 *  (NUBAN). Callers use this to pick a numeric keyboard/placeholder — an
 *  unknown currency defaults to a plain keyboard rather than assuming digits. */
export function isDigitsOnlyAccountCurrency(currency: string): boolean {
  return fixedLengthFor(currency) != null;
}

/**
 * Whether `value` is an acceptable account number for `currency`.
 *
 * NGN must match NUBAN's fixed 10 digits (a shorter/longer entry could not
 * possibly be a real account and would just fail at the bank). Everywhere
 * else, only non-empty is required — never block a valid non-Nigerian
 * account because it isn't 10 digits.
 */
export function isValidAccountNumber(currency: string, value: string): boolean {
  const v = value.trim();
  if (!v) return false;
  const fixed = fixedLengthFor(currency);
  return fixed == null || v.length === fixed;
}

/**
 * Sanitizes a text-input change for the account-number field. NUBAN is
 * digits-only, so NGN strips everything else; other currencies' account
 * numbers can be alphanumeric (e.g. an IBAN), so only whitespace is stripped
 * — digit-only sanitizing would silently mangle a valid non-Nigerian entry.
 */
export function sanitizeAccountNumberInput(currency: string, raw: string): string {
  const cleaned = fixedLengthFor(currency) != null ? raw.replace(/\D/g, "") : raw.replace(/\s/g, "");
  return cleaned.slice(0, accountNumberMaxLength(currency));
}
