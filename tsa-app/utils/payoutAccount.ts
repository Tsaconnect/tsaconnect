// utils/payoutAccount.ts
//
// Rules for the payout/refund account field on the Paycrest buy/sell screens.
//
// Two things decide the rule, in this order:
//
//   1. The DESTINATION KIND. A mobile-money destination wants a phone number,
//      not an account number — every UGX destination is MTN or Airtel, and KES
//      and TZS mix a few in among their banks. The kind comes from Paycrest via
//      the institution list; it cannot be inferred from the currency.
//   2. The CURRENCY, for bank destinations only.
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

/** What the destination actually is, from Paycrest's institution list. */
export type PayoutDestinationKind = 'bank' | 'mobile_money';

/** E.164's ceiling. Mobile-money numbers are entered either locally
 *  (0772123456) or with a country code (256772123456), and we know of no fixed
 *  length that holds for both — so the cap is the only rule applied. */
const MSISDN_MAX_LENGTH = 15;

/** Digits only, 9 to 15 — the range that covers a local or country-code form
 *  without admitting an obvious typo. Mirrored server-side, which is the rule
 *  that actually binds; this one is for the keyboard and the button state. */
const MSISDN_PATTERN = /^\d{9,15}$/;

function fixedLengthFor(currency: string): number | undefined {
  return FIXED_DIGIT_LENGTHS[currency.toUpperCase()];
}

/** A locally-shaped example per corridor. Showing a Ugandan number to a
 *  Kenyan is a small thing, but this field exists precisely because the wrong
 *  hint makes people second-guess a correct entry. */
const MSISDN_PLACEHOLDERS: Record<string, string> = {
  UGX: '0772123456',
  KES: '0722123456',
  TZS: '0754123456',
  NGN: '08031234567',
};

function isMobileMoney(kind?: PayoutDestinationKind): boolean {
  return kind === 'mobile_money';
}

/** Max length to cap a text input at while the user types. */
export function accountNumberMaxLength(currency: string, kind?: PayoutDestinationKind): number {
  if (isMobileMoney(kind)) return MSISDN_MAX_LENGTH;
  return fixedLengthFor(currency) ?? DEFAULT_MAX_LENGTH;
}

/** Whether the field takes digits only. True for NUBAN and for every
 *  mobile-money number; an unknown bank currency defaults to a plain keyboard
 *  rather than assuming digits (an IBAN is alphanumeric).
 *
 *  Named for the FIELD, not the currency: the destination kind decides this as
 *  much as the currency does. */
export function isDigitsOnlyAccountField(
  currency: string,
  kind?: PayoutDestinationKind,
): boolean {
  return isMobileMoney(kind) || fixedLengthFor(currency) != null;
}

/** What to call the field. Asking a Ugandan for an "account number" when the
 *  answer is an MTN phone number is how a payout fails after the on-chain leg
 *  has already moved. */
export function accountFieldLabel(kind?: PayoutDestinationKind): string {
  return isMobileMoney(kind) ? 'Mobile money number' : 'Account number';
}

/** Placeholder for the field, matched to the label. */
export function accountFieldPlaceholder(
  currency: string,
  kind?: PayoutDestinationKind,
): string {
  if (isMobileMoney(kind)) return MSISDN_PLACEHOLDERS[currency.toUpperCase()] ?? 'Phone number';
  return isDigitsOnlyAccountField(currency, kind) ? '0123456789' : 'Account number';
}

/**
 * Whether `value` is an acceptable account number for `currency`.
 *
 * NGN must match NUBAN's fixed 10 digits (a shorter/longer entry could not
 * possibly be a real account and would just fail at the bank). Everywhere
 * else, only non-empty is required — never block a valid non-Nigerian
 * account because it isn't 10 digits.
 */
export function isValidAccountNumber(
  currency: string,
  value: string,
  kind?: PayoutDestinationKind,
): boolean {
  const v = value.trim();
  if (!v) return false;
  // A mobile-money number is not a bank account: NGN's 10-digit NUBAN rule
  // must not be applied to a Nigerian mobile-money destination. No fixed
  // length is safe to assume — users type both the local (0772123456) and
  // country-code (256772123456) forms — but digits and a floor are: nine is
  // below every MSISDN in these corridors, so it rejects a truncated entry
  // without rejecting a real number. "0" passing as valid is how a payout
  // reaches a stranger after the on-chain leg has moved.
  if (isMobileMoney(kind)) return MSISDN_PATTERN.test(v);
  const fixed = fixedLengthFor(currency);
  return fixed == null || v.length === fixed;
}

/**
 * Sanitizes a text-input change for the account-number field. NUBAN is
 * digits-only, so NGN strips everything else; other currencies' account
 * numbers can be alphanumeric (e.g. an IBAN), so only whitespace is stripped
 * — digit-only sanitizing would silently mangle a valid non-Nigerian entry.
 */
export function sanitizeAccountNumberInput(
  currency: string,
  raw: string,
  kind?: PayoutDestinationKind,
): string {
  const digitsOnly = isMobileMoney(kind) || fixedLengthFor(currency) != null;
  const cleaned = digitsOnly ? raw.replace(/\D/g, "") : raw.replace(/\s/g, "");
  return cleaned.slice(0, accountNumberMaxLength(currency, kind));
}

/**
 * Whether a value already typed is still meaningful after the destination
 * changes from `from` to `to`.
 *
 * It is not, whenever the KIND changes. The two are different identifiers for
 * different systems: a phone number is not an account number, and each
 * validates under rules the other would pass by accident. Concretely — in KES
 * or TZS, whose lists mix banks and mobile money — a user types a bank account
 * number, reconsiders, picks M-Pesa, and the number stays in a field now
 * labelled "Mobile money number"; it revalidates, Continue enables, and outside
 * NGN there is no beneficiary-name check to catch it. The payout then goes to
 * whatever that number means on the mobile-money network, after the on-chain
 * leg has already moved.
 *
 * Switching between two banks keeps the value: that is the same kind of
 * identifier, and re-typing an account number to change bank is a real
 * annoyance with no safety benefit.
 */
export function accountValueSurvivesKindChange(
  from?: PayoutDestinationKind,
  to?: PayoutDestinationKind,
): boolean {
  return (from ?? 'bank') === (to ?? 'bank');
}
