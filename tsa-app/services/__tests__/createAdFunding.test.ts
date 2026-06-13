// services/__tests__/createAdFunding.test.ts
//
// Locks down the create-ad publish-gate invariants from the "hide the pool"
// spec (2026-06-05) — the ordering logic that used to live inline in
// create-ad.tsx's handleSubmit, now factored into pure helpers so it is
// testable without a screen-render harness:
//
//   - Funding runs (and is gated on a RESOLVED chain) BEFORE the ad is created
//     — spec §1 "fund first, then create+sign+activate" and "Review fixes" §2.
//   - A 'pending-confirm' funding result ABORTS the publish (no adsApi.create):
//     a broadcast deposit whose confirm failed must not produce an unbacked ad
//     (spec edge-case table).
//   - 'aborted' (e.g. no gas) also stops the publish; only 'already-funded' /
//     'funded' proceed.

import {
  fundingPreflight,
  publishActionForFunding,
} from '../createAdFunding';
import type { EnsureFundingResult } from '../escrowFunding';

describe('fundingPreflight — funding decision + chain gate', () => {
  it('skips funding when available already covers the ad (shortfall <= 0)', () => {
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '100',
        selectedChainId: 56,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'skip' });

    // Overfunded also skips.
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '500',
        selectedChainId: 56,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'skip' });
  });

  it('funds on the resolved chain when there is a shortfall', () => {
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '40',
        selectedChainId: 56,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'fund', chainId: 56 });
  });

  it('treats an unknown available ("-" or empty) as 0 → funds the full amount', () => {
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '-',
        selectedChainId: 56,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'fund', chainId: 56 });

    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '',
        selectedChainId: 56,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'fund', chainId: 56 });
  });

  it('BLOCKS (never funds) when a deposit is needed but no chain is resolved', () => {
    // chains already fetched → ask the seller to pick a network.
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '0',
        selectedChainId: null,
        chainsFetched: true,
      }),
    ).toEqual({ kind: 'block', message: 'Select a network to fund this ad before publishing.' });

    // chains still loading → tell them to wait. undefined is treated like null.
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '0',
        selectedChainId: undefined,
        chainsFetched: false,
      }),
    ).toEqual({ kind: 'block', message: 'Still loading networks — wait a moment and try again.' });
  });

  it('does NOT block on an unresolved chain when no funding is needed (publish straight through)', () => {
    // Single-chain token with covering balance: selectedChainId may be null and
    // that is fine — there is no deposit to broadcast.
    expect(
      fundingPreflight({
        amountBaseUnits: '100',
        available: '100',
        selectedChainId: null,
        chainsFetched: false,
      }),
    ).toEqual({ kind: 'skip' });
  });
});

describe('publishActionForFunding — only a backed ad proceeds', () => {
  it("proceeds on 'already-funded'", () => {
    const r: EnsureFundingResult = { status: 'already-funded' };
    expect(publishActionForFunding(r)).toEqual({ kind: 'proceed' });
  });

  it("proceeds on 'funded'", () => {
    const r: EnsureFundingResult = { status: 'funded', deposited: '60' };
    expect(publishActionForFunding(r)).toEqual({ kind: 'proceed' });
  });

  it("ABORTS the publish on 'aborted' (e.g. no gas) — no ad created", () => {
    const r: EnsureFundingResult = { status: 'aborted', reason: 'no gas' };
    expect(publishActionForFunding(r)).toEqual({ kind: 'abort' });
  });

  it("RECOVERS (stops publish, shows banner) on 'pending-confirm' — never an unbacked ad", () => {
    const r: EnsureFundingResult = {
      status: 'pending-confirm',
      txHash: '0xdep',
      deposited: '60',
    };
    expect(publishActionForFunding(r)).toEqual({ kind: 'recover' });
  });
});
