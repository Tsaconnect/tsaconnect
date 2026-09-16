// utils/swapAddresses.ts
//
// A cross-VM swap needs one address per side, each derived from the wallet's
// mnemonic by its own family (EVM, Solana, Bitcoin). Deciding what to tell the
// user when a side has no address lives here rather than in the All Swap
// screen so it can be tested without mounting a 1,000-line component.

export interface SwapSide {
  /** Display name of the side's chain, e.g. "BTC". */
  chainName: string;
  /** The derived address, or null when there isn't one. */
  address: string | null;
}

/**
 * The message to show while a swap side has no address, or null when both
 * sides are ready.
 *
 * `derived` is what separates the two reasons an address can be missing.
 * Before derivation finishes, null means "still working". After it finishes,
 * null is permanent — a key-only or watch-only wallet has no mnemonic to
 * derive from — and telling that user we are still preparing an address is a
 * promise that never resolves.
 */
export function missingAddressNotice(sides: SwapSide[], derived: boolean): string | null {
  const missing = sides.find(s => !s.address);
  if (!missing) return null;
  return derived
    ? `This wallet has no ${missing.chainName} key. Import its recovery phrase to swap ${missing.chainName}.`
    : `Preparing your ${missing.chainName} address…`;
}
