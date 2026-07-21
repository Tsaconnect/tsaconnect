// utils/rewardForms.ts
//
// Pure validators for the two Reward-programme forms. Kept out of the screens
// so they're directly unit-testable (the validatePmFields pattern from
// merchant-request.tsx).
//
// These mirror the server's rules — the server re-validates everything, since
// the app's locked/unlocked rendering is a convenience, not a security control.
//
// NOTE: this module must stay import-free of the API layer. Importing a VALUE
// from services/* drags in walletApi → AsyncStorage, whose native module is
// null under jest and fails the suite at import time. Values live here; the API
// module re-exports them.

/** Airtime networks the signup reward can pay out to. */
export const PHONE_NETWORKS = ['mtn', 'airtel', 'glo', '9mobile'] as const;
export type PhoneNetwork = (typeof PHONE_NETWORKS)[number];

export const MIN_ONBOARDING_USERNAMES = 10;

// name/username are NOT here: the server derives identity from the auth token
// (accepting them let a user file under someone else's username). The screens
// display them read-only from AuthContext.
export interface SignupFormFields {
  phoneNumber: string;
  phoneNetwork: string;
  inviterUsername: string;
  socialLinks: string;
}

export interface OnboardingFormFields {
  payoutMethod: string;
  payoutDetails: string;
  invitedUsernames: string;
}

export type FormErrors = Record<string, string>;

/** Counts non-blank entries, so trailing separators don't inflate the total. */
export function countUsernames(raw: string): number {
  // Split on commas/newlines ONLY — not spaces, so "John Doe" is one entry —
  // and count distinct lowercased names. Mirrors splitUsernames in the Go
  // handler; both must agree or the client and server disagree on the minimum.
  const seen = new Set<string>();
  for (const part of raw.split(/[,\n\r]+/)) {
    const name = part.trim().toLowerCase();
    if (name) seen.add(name);
  }
  return seen.size;
}

export function validateSignupForm(f: SignupFormFields): FormErrors {
  const errors: FormErrors = {};
  // Nigerian mobile numbers are 11 digits local (0803…) or 13 with the country
  // code. Accept either and let the payout rail normalise.
  const digits = f.phoneNumber.replace(/\D/g, '');
  if (!digits) errors.phoneNumber = 'Enter the phone line for your airtime';
  else if (digits.length < 10 || digits.length > 14) errors.phoneNumber = 'Enter a valid phone number';
  if (!f.phoneNetwork) errors.phoneNetwork = 'Select the network';
  else if (!PHONE_NETWORKS.includes(f.phoneNetwork as never)) errors.phoneNetwork = 'Select a valid network';
  return errors;
}

export function validateOnboardingForm(
  f: OnboardingFormFields,
  minUsernames: number = MIN_ONBOARDING_USERNAMES,
): FormErrors {
  const errors: FormErrors = {};
  if (!f.payoutMethod.trim()) errors.payoutMethod = 'Select how you want to be paid';
  if (!f.payoutDetails.trim()) errors.payoutDetails = 'Enter your account details';
  // Distinct, case-insensitive — matches the server, where repeats don't count.
  const n = countUsernames(f.invitedUsernames);
  if (n < minUsernames) {
    errors.invitedUsernames = `List at least ${minUsernames} different usernames (you have ${n})`;
  }
  return errors;
}
