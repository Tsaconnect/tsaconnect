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

export interface SignupFormFields {
  name: string;
  username: string;
  phoneNumber: string;
  phoneNetwork: string;
  inviterUsername: string;
  socialLinks: string;
}

export interface OnboardingFormFields {
  name: string;
  username: string;
  payoutMethod: string;
  payoutDetails: string;
  invitedUsernames: string;
}

export type FormErrors = Record<string, string>;

/** Counts non-blank entries, so trailing separators don't inflate the total. */
export function countUsernames(raw: string): number {
  return raw
    .split(/[\s,\n]+/)
    .map((s) => s.trim())
    .filter(Boolean).length;
}

export function validateSignupForm(f: SignupFormFields): FormErrors {
  const errors: FormErrors = {};
  if (!f.name.trim()) errors.name = 'Enter your name';
  if (!f.username.trim()) errors.username = 'Enter your username';
  // Nigerian mobile numbers are 11 digits local (0803…) or 13 with the country
  // code. Accept either and let the payout rail normalise.
  const digits = f.phoneNumber.replace(/\D/g, '');
  if (!digits) errors.phoneNumber = 'Enter the phone line for your airtime';
  else if (digits.length < 10 || digits.length > 14) errors.phoneNumber = 'Enter a valid phone number';
  if (!f.phoneNetwork) errors.phoneNetwork = 'Select the network';
  else if (!PHONE_NETWORKS.includes(f.phoneNetwork as never)) errors.phoneNetwork = 'Select a valid network';
  return errors;
}

export function validateOnboardingForm(f: OnboardingFormFields): FormErrors {
  const errors: FormErrors = {};
  if (!f.name.trim()) errors.name = 'Enter your name';
  if (!f.username.trim()) errors.username = 'Enter your username';
  if (!f.payoutMethod.trim()) errors.payoutMethod = 'Select how you want to be paid';
  if (!f.payoutDetails.trim()) errors.payoutDetails = 'Enter your account details';
  const n = countUsernames(f.invitedUsernames);
  if (n < MIN_ONBOARDING_USERNAMES) {
    errors.invitedUsernames = `List at least ${MIN_ONBOARDING_USERNAMES} usernames (you have ${n})`;
  }
  return errors;
}
