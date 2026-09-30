// utils/checkoutPaySelection.ts
//
// Checkout's unified "Pay with" picker (owner decision 2026-09-29) replaces
// the USDC/USDT/MCGP chips + separate "pay with another token" link with one
// selector: pay directly (Sonic USDC/USDT held, or Spendable MCGP) or swap
// another EVM holding into Sonic USDC first. This file is the pure logic
// behind it — what she can pick, and what checkout should default to — so
// every rule is unit-testable without rendering anything.
//
// The swap side always targets Sonic USDC (see checkoutFundingRun): a held
// Sonic USDT is still payable, but only directly, never as a "swap into
// itself".
import type { LiFiToken } from '@/services/lifi';
import { assetFromHolding, type Holding } from '@/services/assetCatalog';
import { fundingPickerAllow } from '@/utils/fundingPicker';

export type PayDirectToken = 'USDC' | 'USDT';

/** What checkout is about to pay with. */
export type PaySelection =
  | { kind: 'direct'; token: PayDirectToken }
  | { kind: 'mcgp' }
  | { kind: 'swap'; token: LiFiToken; chainId: number };

/**
 * The order token an order/quote is created for. A direct payment or a
 * Spendable MCGP debit settles in its own token; a swap always lands Sonic
 * USDC in her wallet first (checkoutFundingRun), so it prices and creates
 * orders in USDC too — never the token she is swapping FROM.
 */
export function orderTokenFor(selection: PaySelection | null): string {
  if (!selection) return 'USDC';
  if (selection.kind === 'mcgp') return 'MCGP';
  if (selection.kind === 'direct') return selection.token;
  return 'USDC';
}

const STABLE_SYMBOLS = new Set([
  'USDC', 'USDT', 'DAI', 'BUSD', 'TUSD', 'USDE', 'USDP', 'FDUSD', 'PYUSD', 'GUSD', 'USDD',
]);

/**
 * A rough "is this a stablecoin" test for ranking swap candidates — not a peg
 * guarantee, just enough to prefer a dollar coin over a volatile one when
 * more than one holding would cover the total.
 */
export const isStableSymbol = (symbol: string): boolean => STABLE_SYMBOLS.has(symbol.toUpperCase());

export interface SwapCandidate {
  token: LiFiToken;
  chainId: number;
  usdValue: number;
}

/**
 * Every EVM holding the "Swap to pay" section may offer: the same rule
 * `fundingPickerAllow` already enforces for "pay with another token" (no
 * wallet MCGP, no non-EVM chain, never the order token itself on Sonic) —
 * fixed to 'USDC' since a swap always targets Sonic USDC now.
 */
export function swapCandidatesFromHoldings(holdings: Holding[]): SwapCandidate[] {
  const allow = fundingPickerAllow('USDC');
  return holdings
    .filter(h => h.balance > 0)
    .map(h => ({ h, token: assetFromHolding(h).networks[0].token }))
    .filter(({ h, token }) => allow(h.chainId, token))
    .map(({ h, token }) => ({ token, chainId: h.chainId, usdValue: h.usdValue }));
}

export interface DefaultSelectionInput {
  /** Sonic USDC held, in USDC units; null while unknown. */
  usdcBalance: number | null;
  /** The cart total in USDC units; null while no quote has priced it yet. */
  usdcNeeded: number | null;
  /** Spendable MCGP held, in MCGP units; null while unknown. */
  mcgpBalance: number | null;
  /** The cart total in MCGP units (the MCGP quote's own total); null while unpriced. */
  mcgpNeeded: number | null;
  /** The cart total in USD, for sizing the swap buffer; null while unpriced. */
  totalUsd: number | null;
  swapCandidates: SwapCandidate[];
}

/**
 * The selection checkout opens with, and re-picks whenever the total changes
 * — but only while the buyer has not chosen for herself (owner decision
 * 2026-09-29, checkout spec §3):
 *
 *   1. Sonic USDC, if she holds enough.
 *   2. Else Spendable MCGP, if she holds enough (in MCGP units).
 *   3. Else the smallest EVM holding that covers the total with a 2% buffer
 *      (LiFi's own slippage/cost margin), preferring a stablecoin over a
 *      volatile one when a stablecoin is sufficient.
 *   4. Else Sonic USDC anyway, so the shortfall is what she sees first.
 *
 * A null figure is "unknown", never "zero" — it is simply skipped, so a slow
 * balance read can never block or mis-pick a default.
 */
export function defaultPaySelection(input: DefaultSelectionInput): PaySelection {
  const { usdcBalance, usdcNeeded, mcgpBalance, mcgpNeeded, totalUsd, swapCandidates } = input;

  if (usdcBalance != null && usdcNeeded != null && usdcBalance >= usdcNeeded) {
    return { kind: 'direct', token: 'USDC' };
  }
  if (mcgpBalance != null && mcgpNeeded != null && mcgpBalance >= mcgpNeeded) {
    return { kind: 'mcgp' };
  }
  if (totalUsd != null) {
    const threshold = totalUsd * 1.02;
    const sufficient = swapCandidates.filter(c => c.usdValue >= threshold);
    if (sufficient.length > 0) {
      const stables = sufficient.filter(c => isStableSymbol(c.token.symbol));
      const pool = stables.length > 0 ? stables : sufficient;
      const best = [...pool].sort((a, b) => a.usdValue - b.usdValue)[0];
      return { kind: 'swap', token: best.token, chainId: best.chainId };
    }
  }
  return { kind: 'direct', token: 'USDC' };
}

/**
 * "Not enough X on Sonic" / "Not enough Spendable MCGP" — always names the
 * chain (or that it is an off-chain balance) and hints at the fix, matching
 * how the swap side already names its own chain in FundingConfirmSheet.
 */
export function payShortfallMessage(input: {
  kind: 'direct' | 'mcgp';
  token?: PayDirectToken;
  neededLabel: string;
  haveLabel: string;
}): string {
  if (input.kind === 'mcgp') {
    return `Not enough Spendable MCGP. You need ${input.neededLabel}; you have ${input.haveLabel}. `
      + `MCGP in your wallet can't pay — try another way to pay.`;
  }
  return `Not enough ${input.token} on Sonic. You need ${input.neededLabel}; you have ${input.haveLabel}. `
    + `Try another way to pay.`;
}
