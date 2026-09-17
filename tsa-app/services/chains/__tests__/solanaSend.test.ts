import { sendSolana, prepareSolana } from '../solanaSend';

// Phantom-path address of M (see derivation.test.ts for vector provenance).
const PHANTOM_ADDR = 'HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk';

// minimal mocks so the flow can run without real network / native modules.
// The derivation preference is pinned to 'phantom' so getSolanaSigner
// resolves without a balance scan (which would consume the fetch mocks).
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn((key: string) =>
    Promise.resolve(key.startsWith('tsa-solana-derivation') ? 'phantom' : 'test-token')
  ),
}));

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
jest.mock('../../wallet', () => ({
  getMnemonic: jest.fn().mockResolvedValue(M),
}));

function mockFetchOnce(responses: Array<{ ok: boolean; status?: number; body: any }>) {
  const fn = jest.fn();
  for (const r of responses) {
    fn.mockResolvedValueOnce({
      ok: r.ok,
      status: r.status ?? (r.ok ? 200 : 400),
      text: async () => JSON.stringify(r.body),
    });
  }
  global.fetch = fn as unknown as typeof fetch;
  return fn;
}

describe('sendSolana', () => {
  it("throws 'prepare-tx returned no payloads' when prepared has no payloads array", async () => {
    mockFetchOnce([{ ok: true, body: { data: { prepared: {} } } }]);
    await expect(
      sendSolana({ from: 'So1', to: 'So2', amount: '1', symbol: 'SOL', walletAddress: '0xw' })
    ).rejects.toThrow('prepare-tx returned no payloads');
  });

  it('prepares, signs the message with the ed25519 Solana key, and broadcasts to a txHash', async () => {
    const prepared = {
      family: 'solana',
      raw: 'deadbeef',
      payloads: [{ index: 0, hashHex: 'deadbeef', curve: 'ed25519' }],
    };
    const fetchFn = mockFetchOnce([
      { ok: true, body: { data: { prepared, fee: { hasEnoughGas: true } } } },
      { ok: true, body: { data: { txHash: 'SoLtxHash123' } } },
    ]);

    const res = await sendSolana({
      from: PHANTOM_ADDR, to: 'SoLrecipient', amount: '1000000000', symbol: 'SOL', walletAddress: '0xw',
    });

    expect(res.txHash).toBe('SoLtxHash123');
    // second call is submit-tx and carries a 64-byte ed25519 signature (128 hex)
    const submitBody = JSON.parse((fetchFn.mock.calls[1][1] as any).body);
    expect(submitBody.sigs[0].sigHex).toMatch(/^[0-9a-f]{128}$/);
  });

  it('refuses to sign a tx prepared for a different Solana address', async () => {
    const prepared = { family: 'solana', raw: 'deadbeef', payloads: [{ index: 0, hashHex: 'deadbeef', curve: 'ed25519' }] };
    const fetchFn = mockFetchOnce([{ ok: true, body: { data: { prepared, fee: { hasEnoughGas: true } } } }]);
    await expect(
      sendSolana({ from: 'SoLotherPath', to: 'SoLrecipient', amount: '1', symbol: 'SOL', walletAddress: '0xw' })
    ).rejects.toThrow('Your Solana address changed');
    expect(fetchFn).toHaveBeenCalledTimes(1); // never reached submit-tx
  });
});

describe('prepareSolana gas preflight', () => {
  it('surfaces the backend note when the sender lacks SOL for gas + ATA rent (402)', async () => {
    mockFetchOnce([
      { ok: false, status: 402, body: { message: 'You need SOL in this wallet to send USDC (network fee + account rent).' } },
    ]);
    await expect(
      prepareSolana({ from: 'So1', to: 'So2', amount: '1000000', symbol: 'USDC', tokenAddr: 'EPjF...', walletAddress: '0xw' })
    ).rejects.toThrow('You need SOL in this wallet to send USDC (network fee + account rent).');
  });
});
