// utils/walletSetup.ts
//
// The pure part of setting up a wallet (app/wallet/seedphrase.tsx): which path
// the user chose, whether creating a new wallet would replace one their account
// already has, and whether their answers prove they wrote the phrase down.

export type WalletSetupMode = 'create' | 'import';

/** The `mode` a screen was opened with; anything else is "let me choose". */
export function parseSetupMode(raw: string | string[] | undefined): WalletSetupMode | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return value === 'create' || value === 'import' ? value : undefined;
}

/**
 * The account has a wallet linked that this device holds no copy of — a user
 * on a new phone. Creating a wallet there moves the account's link to the new
 * one, away from wherever their funds are, so they are warned first.
 */
export function linkedWalletNotOnDevice(
  linked: string | null | undefined,
  localAddresses: string[],
): boolean {
  if (!linked) return false;
  return !localAddresses.some((a) => a.toLowerCase() === linked.toLowerCase());
}

export const SEED_WORD_COUNT = 12;
export const VERIFY_WORD_COUNT = 4;

export type BackupCheck =
  /** There is no phrase on screen or no question was asked: nothing was proven. */
  | 'unprepared'
  /** Some positions have no answer yet. */
  | 'incomplete'
  | 'wrong'
  | 'correct';

/**
 * Whether the answers prove the user has the phrase. A backup is confirmed only
 * on `correct`: an empty set of questions proves nothing, and used to pass.
 */
export function checkBackupAnswers(
  words: string[],
  askedIndices: number[],
  answers: Record<number, string>,
): BackupCheck {
  if (words.length !== SEED_WORD_COUNT || askedIndices.length !== VERIFY_WORD_COUNT) return 'unprepared';
  if (askedIndices.some((i) => !answers[i])) return 'incomplete';
  return askedIndices.every((i) => answers[i] === words[i]) ? 'correct' : 'wrong';
}
