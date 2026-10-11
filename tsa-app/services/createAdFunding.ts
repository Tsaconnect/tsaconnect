// services/createAdFunding.ts
//
// Pure decision helpers for the "hide the pool" create-ad publish gate
// (spec 2026-06-05). They encode the two ordering invariants that MUST hold
// around ensureEscrowFunding, factored out of create-ad.tsx's handleSubmit so
// they can be unit-tested without a screen-render harness:
//
//   1. fundingPreflight — given the ad amount vs the seller's on-chain
//      `available` and whether a concrete chain is resolved, decide whether
//      funding is needed and, if so, whether we may proceed (a real deposit
//      broadcast must never run on an unresolved/defaulted chain — spec
//      "Review fixes" §2). If funding is needed but no chain is resolved, the
//      caller must BLOCK publish with `message`.
//
//   2. publishActionForFunding — map an EnsureFundingResult to the next step:
//      only 'already-funded' / 'funded' may proceed to create+sign+activate.
//      'aborted' (e.g. no gas) and 'pending-confirm' (deposit on-chain but its
//      confirm POST failed) MUST stop the publish — we never create an unbacked
//      ad (spec edge-case table + §1 "fund first, then create+sign+activate").
//
// Framework-free: no React, no I/O — pure BigInt/string logic so handleSubmit
// stays a thin orchestrator over tested decisions.

import type { EnsureFundingResult } from './escrowFunding';
import { formatTokenAmount } from './p2p';
import { p2pCaughtMessage, p2pErrorMessage, rawErrorText, type P2PErrorLike } from './p2pErrors';
import { decodeRevertError } from './txErrors';

export interface FundingPreflightInput {
  /** Base units the ad must be backed by (already decimal-scaled by caller). */
  amountBaseUnits: string;
  /** Seller's current on-chain `available`, base units. null/'-'/'' = unknown → 0. */
  available: string | null | undefined;
  /** Resolved chain id, or null/undefined when not yet resolved. */
  selectedChainId: number | null | undefined;
  /** Whether the /p2p/chains fetch has landed (drives the block message). */
  chainsFetched: boolean;
}

export type FundingPreflight =
  // No deposit required — publish straight through (shortfall <= 0).
  | { kind: 'skip' }
  // Deposit required and a concrete chain is resolved — run ensureEscrowFunding.
  | { kind: 'fund'; chainId: number }
  // Deposit required but no concrete chain — caller must throw `message`.
  | { kind: 'block'; message: string };

/**
 * Decide whether funding is needed and whether we're allowed to broadcast it.
 * Mirrors create-ad's old inline gate exactly (need > have; block when funding
 * is required without a resolved chain), with no behavior change.
 */
export function fundingPreflight(input: FundingPreflightInput): FundingPreflight {
  const { amountBaseUnits, available, selectedChainId, chainsFetched } = input;

  const need = BigInt(amountBaseUnits);
  const have = available && available !== '-' ? BigInt(available) : 0n;
  const fundingNeeded = need > have;

  if (!fundingNeeded) {
    return { kind: 'skip' };
  }

  if (selectedChainId == null) {
    return {
      kind: 'block',
      message: chainsFetched
        ? 'Select a network to fund this ad before publishing.'
        : 'Still loading networks — wait a moment and try again.',
    };
  }

  return { kind: 'fund', chainId: selectedChainId };
}

export type PublishAction =
  // Safe to create+sign+activate the ad — it is fully backed.
  | { kind: 'proceed' }
  // Stop the publish cleanly; nothing dangling (e.g. gas abort).
  | { kind: 'abort' }
  // Deposit is on-chain & safe but its confirm failed — show recovery, do NOT
  // publish an unbacked ad. Caller surfaces the banner and stops.
  | { kind: 'recover' };

/**
 * Map an ensureEscrowFunding result to the publish decision. Only a covered or
 * freshly-funded balance proceeds to publish; everything else stops. This is
 * the invariant that guarantees we never create an ad we haven't backed.
 */
export function publishActionForFunding(result: EnsureFundingResult): PublishAction {
  switch (result.status) {
    case 'already-funded':
    case 'funded':
      return { kind: 'proceed' };
    case 'pending-confirm':
      return { kind: 'recover' };
    case 'aborted':
      return { kind: 'abort' };
  }
}

/**
 * The "Could not publish ad" body for a failed publish (KYC refusals are routed
 * to the Verify prompt before this). An opaque on-chain revert — e.g. the
 * ERC20InsufficientBalance hex the backend surfaces when the wallet can't cover
 * the deposit — becomes an actionable sentence; anything else goes through
 * p2pCaughtMessage, so a raw server or RPC string never reaches the screen.
 */
export function publishFailureMessage(
  err: unknown,
  p: { symbol: string; decimals: number; depositedThisAttempt: boolean },
): string {
  const { symbol: sym, decimals } = p;
  // Decode the original text: escrow funding throws mapped copy, keeping the
  // server/RPC string (with the revert hex) as `raw`.
  const revert = decodeRevertError(rawErrorText(err));
  let body: string;
  if (revert?.kind === 'insufficient-balance') {
    body =
      `Your wallet doesn't have enough ${sym} to back this ad. It needs ` +
      `${formatTokenAmount(revert.needed, decimals, 4)} ${sym}, but your wallet holds ` +
      `${formatTokenAmount(revert.balance, decimals, 4)} ${sym} on this network. ` +
      `Top up your ${sym}, then publish.`;
  } else if (revert?.kind === 'insufficient-allowance') {
    body =
      `The escrow isn't approved to move your ${sym} yet. Please try publishing ` +
      `again — the approval is requested first.`;
  } else if (revert?.kind === 'unknown-revert') {
    body =
      `The deposit can't go through right now — your wallet may be missing the ${sym} ` +
      `or the network gas needed. Check your balances and try again.`;
  } else {
    body = p2pCaughtMessage(err);
  }
  // Only mention a safe deposit when one actually broadcast+confirmed this
  // attempt; an insufficient-balance / gas-estimate revert moves no funds.
  if (p.depositedThisAttempt) {
    body +=
      '\n\nThe deposit you just made is safe in your escrow balance — retry to publish, ' +
      'or reclaim it from My Ads.';
  }
  return body;
}

// The statusError copy createAndSignAd throws as a P2PCopyError (shown as-is),
// so it must only ever be app copy or p2pErrorMessage's mapping — never a
// server message passed through.

/**
 * A create/sign refusal on the sell-ad publish, as actionable copy. 410 (Gone)
 * is the Phase 2B migration sentinel for stale clients calling removed legacy
 * endpoints; 503 fires when the rate feed is stale mid-publish (float ads need
 * a fresh feed). Anything else goes through the shared P2P copy (e.g.
 * rate_out_of_band → "Rate must be between X and Y").
 */
export function createAdStatusError(status: number | undefined, message: string | undefined, res?: P2PErrorLike): string {
  if (status === 410) {
    return 'Your app version is out of date — please update and retry.';
  }
  if (status === 503) {
    return 'Live rate just expired. Refresh the screen and try again.';
  }
  // Server-side EIP-712 recovery mismatch — the wallet that produced the
  // signature derives to a different address than the one we declared as
  // sellerAddress. The pre-flight in handleSubmit catches the common
  // SecureStore-corruption case, but if a stale build slips through (or the
  // active wallet flips mid-flow) we still want a useful message instead of
  // the raw "signature recovered to 0x… expected 0x…" — which only goes to
  // the log, for support.
  if (message && /signature recovered to /i.test(message)) {
    console.warn('[createAd] voucher signature mismatch:', message);
    return (
      'Your wallet did not sign the ad correctly — its key does not match ' +
      'the address on the ad. Remove this wallet and re-import it from its ' +
      'seed phrase, then retry.'
    );
  }
  return p2pErrorMessage(res ?? { status, message }, 'Server rejected the request');
}

/** LP-specific HTTP copy (review M4): a 503 here is the pool closing or the
 *  chain being unreachable, never a stale rate. */
export function lpStatusError(status?: number, message?: string, res?: P2PErrorLike): string {
  if (status === 503) return 'The pool is closed or the network is busy right now. Your funds are safe; try again later.';
  if (status === 409) return p2pErrorMessage(res ?? { status, message }, 'You already have a live pool ad.');
  return createAdStatusError(status, message, res);
}
