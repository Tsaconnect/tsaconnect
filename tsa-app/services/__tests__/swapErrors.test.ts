import { mapSubmitError, txHashOrThrow, SWAP_COPY, TRANSFER_COPY } from '../swapErrors';

// Verbatim from a production Arbitrum rejection, shown to a user in a red box
// on the Sell for Naira screen.
const RAW_BASE_FEE =
  'Failed to submit transaction to blockchain on arbitrum: failed to send transaction: ' +
  'max fee per gas less than block base fee: address 0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce, ' +
  'maxFeePerGas: 20006000 baseFee: 20098000';

// Short enough to clear MAX_RAW_LEN, so matching it proves the branch exists
// rather than the length guard firing.
const SHORT_BASE_FEE = 'err: fee cap less than block base fee';

describe('mapSubmitError', () => {
  it('turns a stale base fee into copy with no RPC internals in it', () => {
    const msg = mapSubmitError(RAW_BASE_FEE);

    expect(msg).toContain('Network fees rose while you were confirming.');
    expect(msg).not.toMatch(/0x[0-9a-fA-F]{8}/);
    expect(msg.toLowerCase()).not.toContain('maxfeepergas');
  });

  it('matches the same rejection when it is short enough to pass the length guard', () => {
    expect(mapSubmitError(SHORT_BASE_FEE)).toContain('Network fees rose while you were confirming.');
  });

  it('never leaks an address or hex blob for any known error', () => {
    const raws = [
      RAW_BASE_FEE,
      SHORT_BASE_FEE,
      'nonce too low: address 0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce, tx: 5 state: 7',
      'replacement transaction underpriced',
      'in-flight transaction limit reached for delegated accounts',
      'insufficient funds for gas * price + value: address 0xabc123def456',
      'already known',
    ];
    for (const raw of raws) {
      expect(mapSubmitError(raw)).not.toMatch(/0x[0-9a-fA-F]{6}/);
    }
  });

  // The tx is in the mempool and will mine. Calling it a failure invites the
  // user to send a duplicate.
  it('does not report an already-submitted transaction as a failure', () => {
    const msg = mapSubmitError('already known');

    expect(msg).toContain('already been submitted');
    expect(msg.toLowerCase()).not.toContain('failed');
  });

  describe('per-flow copy', () => {
    it('tells a swap to refresh its quote, because re-submitting a stale one can fill at the wrong price', () => {
      expect(mapSubmitError(SHORT_BASE_FEE, SWAP_COPY)).toContain('Refresh the quote');
      expect(mapSubmitError('nonce too low', SWAP_COPY)).toContain('Refresh the quote');
    });

    it('does not tell a transfer to refresh a quote it does not have', () => {
      expect(mapSubmitError(SHORT_BASE_FEE, TRANSFER_COPY)).not.toContain('quote');
      expect(mapSubmitError(SHORT_BASE_FEE, TRANSFER_COPY)).toContain('Please try again.');
    });

    it('defaults to swap copy, so an un-migrated caller keeps its old wording', () => {
      expect(mapSubmitError(SHORT_BASE_FEE)).toBe(mapSubmitError(SHORT_BASE_FEE, SWAP_COPY));
    });

    it('uses the flow\'s own fallback, so a transfer is not called a swap', () => {
      const internal = 'sql: ' + 'x'.repeat(300);

      expect(mapSubmitError(internal, TRANSFER_COPY)).toBe(TRANSFER_COPY.fallback);
      expect(mapSubmitError(internal, SWAP_COPY)).toContain('Swap failed');
    });
  });

  // Callers write `json?.message ? mapSubmitError(...) : <status fallback>`
  // rather than `mapSubmitError(...) || <status fallback>`, because this never
  // returns something falsy for the `||` to catch.
  it('always returns a non-empty string', () => {
    for (const raw of ['', null, undefined, '   ', 'anything at all']) {
      expect(mapSubmitError(raw, TRANSFER_COPY).length).toBeGreaterThan(0);
    }
  });

  it('passes a short unrecognised message through unchanged', () => {
    expect(mapSubmitError('This order has no deposit address yet.')).toBe(
      'This order has no deposit address yet.',
    );
  });

  it('shows a revert reason but still caps an ABI blob', () => {
    expect(mapSubmitError('execution reverted: transfer to the zero address')).toBe(
      'execution reverted: transfer to the zero address',
    );
    expect(mapSubmitError('execution reverted: 0x' + 'a'.repeat(400), TRANSFER_COPY)).toBe(
      TRANSFER_COPY.fallback,
    );
  });
});

describe('txHashOrThrow', () => {
  it('returns the hash when the broadcast succeeded', () => {
    expect(txHashOrThrow({ success: true, data: { txHash: '0xdeadbeef' } })).toBe('0xdeadbeef');
  });

  it('throws mapped copy, never the RPC message', () => {
    expect(() => txHashOrThrow({ success: false, message: RAW_BASE_FEE })).toThrow(
      'Network fees rose while you were confirming. Please try again.',
    );
  });

  // A success with no hash is not a success — nothing downstream can track it.
  it('throws when the response claims success but carries no hash', () => {
    expect(() => txHashOrThrow({ success: true, data: null })).toThrow(TRANSFER_COPY.fallback);
  });
});
