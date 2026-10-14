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

//   - publishFailureMessage never shows a raw server/RPC string (app copy
//     passes through as-is; an undecodable revert gets an actionable sentence).

// services/p2p.ts (formatTokenAmount) pulls in the auth headers' storage.
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn(), setItem: jest.fn() }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import {
  createAdStatusError,
  fundingPreflight,
  publishActionForFunding,
  publishFailureMessage,
} from '../createAdFunding';
// eslint-disable-next-line import/first
import type { EnsureFundingResult } from '../escrowFunding';
// eslint-disable-next-line import/first
import { P2PCopyError, p2pErrorMessage } from '../p2pErrors';

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

describe('publishFailureMessage — what "Could not publish ad" says', () => {
  const opts = { symbol: 'USDT', decimals: 18, depositedThisAttempt: false };
  const word = (v: bigint) => v.toString(16).padStart(64, '0');

  it('maps a raw server/RPC message instead of showing it', () => {
    const raw = 'p2p: prepare deposit: ' + 'x'.repeat(250);
    expect(publishFailureMessage(new Error(raw), opts)).toBe('Something went wrong. Please try again.');
    expect(publishFailureMessage(new Error('p2p: ad is not active'), opts)).toBe('This ad is no longer available.');
  });

  it('keeps the timeout copy for an approve that has not confirmed yet', () => {
    expect(publishFailureMessage(new Error('Approve confirmation timed out'), opts)).toMatch(/may still go through/);
  });

  it('shows app copy as-is, however long', () => {
    const copy = 'Your wallet did not sign the ad correctly — ' + 'its key does not match. '.repeat(10);
    expect(publishFailureMessage(new P2PCopyError(copy), opts)).toBe(copy);
  });

  it('decodes an insufficient-balance revert first', () => {
    const msg = 'execution reverted: 0xe450d38c' + word(1n) + word(0n) + word(25n * 10n ** 18n);
    expect(publishFailureMessage(new Error(msg), opts)).toMatch(/needs 25 USDT, but your wallet holds 0 USDT/);
  });

  it('adds the deposit-is-safe line only when a deposit landed this attempt', () => {
    expect(publishFailureMessage(new Error('boom'), opts)).not.toMatch(/safe in your escrow/);
    expect(publishFailureMessage(new Error('boom'), { ...opts, depositedThisAttempt: true })).toMatch(
      /^Boom\n\nThe deposit you just made is safe in your escrow balance/,
    );
  });
});

// statusError output is thrown as a P2PCopyError and shown as-is, so it must be
// app copy or p2pErrorMessage's mapping — never the server text passed through.
describe('createAdStatusError — never the raw server message', () => {
  it('signature mismatch: actionable copy without the server message', () => {
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    const raw = 'signature recovered to 0x1111111111111111111111111111111111111111, expected 0x2222222222222222222222222222222222222222';
    const copy = createAdStatusError(400, raw);
    expect(copy).toMatch(/^Your wallet did not sign the ad correctly/);
    expect(copy).not.toContain('0x1111');
    expect(copy).not.toMatch(/server message/i);
    warn.mockRestore();
  });

  it.each([
    [400, 'p2p: amount outside ad min/max'],
    [500, '<html>Internal Server Error</html>'],
    [400, 'pq: duplicate key value violates unique constraint; sql: ' + 'x'.repeat(50)],
  ])('%s %s: exactly the shared mapping', (status, message) => {
    const res = { status, message };
    expect(createAdStatusError(status, message, res)).toBe(p2pErrorMessage(res, 'Server rejected the request'));
  });
});
