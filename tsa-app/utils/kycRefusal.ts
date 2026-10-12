// utils/kycRefusal.ts
//
// The server refuses every money-in path (MCGP buy, stablecoin buy, virtual
// account activation, opening a P2P trade as the buyer) for a user it cannot
// attribute a bank transfer to. It says so with one of two machine codes, and
// the two need different next steps:
//
//   - kyc_verification_required: the user has not passed verification. They can
//     fix that themselves, so send them to the KYC screen.
//   - kyc_legal_name_missing: verification passed but left no legal name. The
//     KYC screen already reads "verified" and a new session is refused, so
//     routing there is a dead end — only support can repair it.
//
// Branch on the code, never the message.
import { Alert } from 'react-native';
import { router } from 'expo-router';

export const KYC_VERIFICATION_REQUIRED = 'kyc_verification_required';
export const KYC_LEGAL_NAME_MISSING = 'kyc_legal_name_missing';

/** The Didit verification screen. */
export const KYC_ROUTE = '/profile/kyc';

/** 'p2p' swaps the pay-in wording, which is wrong for a seller. */
export type KycContext = 'p2p';

export type KycRefusal =
  | { kind: 'verify'; title: string; message: string }
  | { kind: 'support'; title: string; message: string };

/** Describes the prompt for a refusal code, or null when the code is not one. */
export function kycRefusalFor(
  code?: string,
  serverMessage?: string,
  context?: KycContext,
): KycRefusal | null {
  switch (code) {
    case KYC_VERIFICATION_REQUIRED:
      return {
        kind: 'verify',
        title: 'Verify your identity',
        message:
          context === 'p2p'
            ? 'You need to finish identity verification before you can trade on P2P. It only takes a few minutes.'
            : 'You need to finish identity verification before you can pay in. It only takes a few minutes.',
      };
    case KYC_LEGAL_NAME_MISSING:
      return {
        kind: 'support',
        title: 'Contact support',
        // P2P refusals come from many routes (gas, ads, trades) whose wording
        // is not written for users, so P2P always shows its own copy.
        message:
          context === 'p2p'
            ? 'Your identity is verified, but we could not confirm the legal name on it. Contact support to finish setting up P2P.'
            : serverMessage ||
              'Your identity is verified, but we could not confirm the legal name on it. Contact support to finish setting up payments.',
      };
    default:
      return null;
  }
}

/**
 * Shows the right prompt when a response carries a KYC refusal code and returns
 * true, so the caller can stop. Returns false for any other response.
 */
export function promptKycRefusal(code?: string, serverMessage?: string, context?: KycContext): boolean {
  const refusal = kycRefusalFor(code, serverMessage, context);
  if (!refusal) return false;
  if (refusal.kind === 'verify') {
    Alert.alert(refusal.title, refusal.message, [
      { text: 'Later', style: 'cancel' },
      { text: 'Verify now', onPress: () => router.push(KYC_ROUTE as never) },
    ]);
  } else {
    Alert.alert(refusal.title, refusal.message);
  }
  return true;
}
