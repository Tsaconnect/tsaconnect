// services/buyerId.ts
//
// The buyer's BVN or NIN and date of birth, which Paycrest requires on every
// bank-transfer purchase (source.kyc). Asked for once, then kept ONLY on this
// phone, in the secure store (Keychain / Keystore), per wallet. Each Paycrest
// buy sends it; the server passes it to Paycrest and never stores it. Each
// entry records the account it belongs to: another account signed in on the
// same phone and wallet reads it as none and is asked for its own.

import * as SecureStore from 'expo-secure-store';

export type BuyerIdType = 'bvn' | 'nin';

/** API codes: no ID sent, a malformed one, or one Paycrest refused. */
export const BUYER_ID_REQUIRED = 'buyer_id_required';
export const BUYER_ID_INVALID = 'buyer_id_invalid';
export const BUYER_ID_REJECTED = 'buyer_id_rejected';

/** As the API takes it: birthDate is YYYY-MM-DD. */
export interface BuyerId {
  type: BuyerIdType;
  number: string;
  birthDate: string;
}

const keyFor = (wallet: string) => `buyerId.v1.${wallet.toLowerCase()}`;

const OPTIONS: SecureStore.SecureStoreOptions = {
  keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
};

/**
 * The ID saved for this wallet by this account, or null. Another account's
 * entry, or a damaged one, reads as none.
 */
export async function readBuyerId(wallet: string, account: string): Promise<BuyerId | null> {
  if (!wallet || !account) return null;
  try {
    const raw = await SecureStore.getItemAsync(keyFor(wallet), OPTIONS);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<BuyerId> & { account?: string };
    if (
      v.account === account &&
      (v.type === 'bvn' || v.type === 'nin') &&
      typeof v.number === 'string' &&
      typeof v.birthDate === 'string'
    ) {
      return { type: v.type, number: v.number, birthDate: v.birthDate };
    }
  } catch {
    // Unreadable: ask again rather than send something broken.
  }
  return null;
}

export async function saveBuyerId(wallet: string, account: string, id: BuyerId): Promise<void> {
  if (!wallet || !account) throw new Error('no wallet or account to save the ID under');
  await SecureStore.setItemAsync(keyFor(wallet), JSON.stringify({ ...id, account }), OPTIONS);
}

export async function forgetBuyerId(wallet: string): Promise<void> {
  try {
    await SecureStore.deleteItemAsync(keyFor(wallet), OPTIONS);
  } catch {
    // Nothing saved, or the store is unavailable: nothing to forget.
  }
}

/** Typing "15041990" shows "15/04/1990". */
export function formatBirthDateInput(text: string): string {
  const d = text.replace(/\D/g, '').slice(0, 8);
  if (d.length <= 2) return d;
  if (d.length <= 4) return `${d.slice(0, 2)}/${d.slice(2)}`;
  return `${d.slice(0, 2)}/${d.slice(2, 4)}/${d.slice(4)}`;
}

/** "1990-04-15" back to the "15/04/1990" the form shows. */
export function birthDateForInput(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  return m ? `${m[3]}/${m[2]}/${m[1]}` : '';
}

export type BuyerIdField = 'number' | 'birthDate';

/**
 * Checks the form the way the server does (11 digits; a real date; 18 or
 * over) so a mistake is shown on the field, not after a round trip.
 */
export function parseBuyerId(
  input: { type: BuyerIdType; number: string; birthDate: string },
  now: Date = new Date(),
): { id: BuyerId } | { field: BuyerIdField; message: string } {
  const number = input.number.replace(/\s/g, '');
  if (!/^\d{11}$/.test(number)) {
    return { field: 'number', message: `Your ${input.type.toUpperCase()} is 11 digits.` };
  }
  const m = /^(\d{2})\/(\d{2})\/(\d{4})$/.exec(input.birthDate.trim());
  if (!m) return { field: 'birthDate', message: 'Enter your date of birth as DD/MM/YYYY.' };
  const [day, month, year] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const born = new Date(Date.UTC(year, month - 1, day));
  if (born.getUTCFullYear() !== year || born.getUTCMonth() !== month - 1 || born.getUTCDate() !== day) {
    return { field: 'birthDate', message: 'That date does not exist.' };
  }
  const adult = new Date(Date.UTC(year + 18, month - 1, day));
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  if (adult.getTime() > today) return { field: 'birthDate', message: 'You must be 18 or over to buy.' };
  if (year < now.getUTCFullYear() - 120) return { field: 'birthDate', message: 'Check your date of birth.' };
  return { id: { type: input.type, number, birthDate: `${m[3]}-${m[2]}-${m[1]}` } };
}
