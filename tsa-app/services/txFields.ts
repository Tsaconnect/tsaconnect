// services/txFields.ts
//
// One notion of "this field is absent" for unsigned transactions, shared by
// every layer that reads one.
//
// It lives in its own module because two guards enforcing the same invariant
// with different definitions is worse than one guard: walletApi's normalizer
// rejected '' and NaN while signTransaction only rejected undefined and null,
// so an empty chainId — which is exactly what Go emits for an unset
// `ChainID string`, and what the order refund path passes through unnormalized
// — cleared the chokepoint and signed for chain 0.

/**
 * A number that was actually present on the wire, or null.
 *
 * Rejects undefined, null, '' and anything non-finite (NaN from arithmetic on
 * an absent field, Infinity). Accepts 0, which is a real nonce — a wallet's
 * first transaction — and must not be confused with a missing one.
 */
export function toFiniteNumber(value: unknown): number | null {
  if (value === null || value === undefined || value === '') {
    return null;
  }
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}
