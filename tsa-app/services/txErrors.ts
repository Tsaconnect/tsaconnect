// services/txErrors.ts
//
// Decode the common on-chain revert payloads that the backend surfaces verbatim
// as "...execution reverted: 0x…" when a deposit/approve would fail during gas
// estimation, so the UI can show a clear, actionable message instead of raw ABI
// hex (e.g. the OpenZeppelin v5 ERC-20 custom errors).
//
// Pure + framework-free: returns the decoded amounts as base-unit strings; the
// caller formats them with the right per-chain decimals and writes the copy.

export type RevertInfo =
  // OZ ERC20InsufficientBalance(address sender, uint256 balance, uint256 needed)
  | { kind: 'insufficient-balance'; balance: string; needed: string }
  // OZ ERC20InsufficientAllowance(address spender, uint256 allowance, uint256 needed)
  | { kind: 'insufficient-allowance'; allowance: string; needed: string }
  // A revert we recognise as such but can't decode into named fields.
  | { kind: 'unknown-revert' };

// First 4 bytes of keccak256(errorSignature).
const SEL_INSUFFICIENT_BALANCE = 'e450d38c'; // ERC20InsufficientBalance(address,uint256,uint256)
const SEL_INSUFFICIENT_ALLOWANCE = 'fb8f41b2'; // ERC20InsufficientAllowance(address,uint256,uint256)

/**
 * Parse a backend/RPC error string. Returns null when the message is not a
 * revert (so the caller falls back to the raw text); otherwise a best-effort
 * decode. Never throws.
 */
export function decodeRevertError(message: string | undefined | null): RevertInfo | null {
  const m = message ?? '';
  // Only engage on messages that look like an on-chain revert / failed estimate.
  if (!/revert|estimate gas|would revert/i.test(m)) return null;

  const hex = m.match(/0x[0-9a-fA-F]{8,}/);
  if (!hex) return { kind: 'unknown-revert' };

  const data = hex[0].slice(2);
  const selector = data.slice(0, 8).toLowerCase();
  // 32-byte word `i` after the 4-byte selector, as a decimal base-unit string.
  const word = (i: number): string | null => {
    const slice = data.slice(8 + i * 64, 8 + (i + 1) * 64);
    if (slice.length < 64) return null;
    try {
      return BigInt('0x' + slice).toString();
    } catch {
      return null;
    }
  };

  if (selector === SEL_INSUFFICIENT_BALANCE) {
    const balance = word(1);
    const needed = word(2);
    if (balance != null && needed != null) return { kind: 'insufficient-balance', balance, needed };
  }
  if (selector === SEL_INSUFFICIENT_ALLOWANCE) {
    const allowance = word(1);
    const needed = word(2);
    if (allowance != null && needed != null) return { kind: 'insufficient-allowance', allowance, needed };
  }
  return { kind: 'unknown-revert' };
}
