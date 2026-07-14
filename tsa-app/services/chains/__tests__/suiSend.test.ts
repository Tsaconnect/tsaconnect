import { sendSui, prepareSui } from '../suiSend';
import { deriveSuiPublicKey } from '../derivation';

// minimal mocks so the flow can run without real network / native modules
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('test-token'),
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

describe('sendSui', () => {
  it("throws 'prepare-tx returned no payloads' when prepared has no payloads array", async () => {
    mockFetchOnce([{ ok: true, body: { data: { prepared: {} } } }]);
    await expect(
      sendSui({ from: '0x1', to: '0x2', amount: '1', symbol: 'SUI', walletAddress: '0xw' })
    ).rejects.toThrow('prepare-tx returned no payloads');
  });

  it('signs the intent digest and packs pubkey||sig (pubkey first) for the backend', async () => {
    const prepared = {
      family: 'sui',
      // a valid 32-byte digest hex (what the backend puts in hashHex)
      raw: 'ZGVhZGJlZWY=',
      payloads: [{ index: 0, hashHex: '74202195b64a7fec6f90c304ec5ea2996641d0eb7877d3a98defefa47229a9f9', curve: 'ed25519' }],
    };
    const fetchFn = mockFetchOnce([
      { ok: true, body: { data: { prepared, fee: { hasEnoughGas: true } } } },
      { ok: true, body: { data: { txHash: 'SuiDigest123' } } },
    ]);

    const res = await sendSui({
      from: '0xsender', to: '0xrecipient', amount: '1000000000', symbol: 'SUI', walletAddress: '0xw',
    });

    expect(res.txHash).toBe('SuiDigest123');
    const submitBody = JSON.parse((fetchFn.mock.calls[1][1] as any).body);
    // sigHex = pubkey(64 hex) || sig(128 hex) = 192 hex chars, pubkey FIRST.
    expect(submitBody.sigs[0].sigHex).toMatch(/^[0-9a-f]{192}$/);
    expect(submitBody.sigs[0].sigHex.slice(0, 64)).toBe(deriveSuiPublicKey(M));
  });

  it('rejects a non-ed25519 payload curve', async () => {
    const prepared = {
      family: 'sui',
      raw: 'ZGVhZGJlZWY=',
      payloads: [{ index: 0, hashHex: 'deadbeef', curve: 'secp256k1' }],
    };
    mockFetchOnce([{ ok: true, body: { data: { prepared, fee: {} } } }]);
    await expect(
      sendSui({ from: '0x1', to: '0x2', amount: '1', symbol: 'SUI', walletAddress: '0xw' })
    ).rejects.toThrow('unexpected signing curve: secp256k1');
  });
});

describe('prepareSui gas preflight', () => {
  it('surfaces the backend note when the sender lacks SUI for gas (402)', async () => {
    mockFetchOnce([
      { ok: false, status: 402, body: { message: 'You need SUI in this wallet to send USDC (network fee).' } },
    ]);
    await expect(
      prepareSui({ from: '0x1', to: '0x2', amount: '1000000', symbol: 'USDC', tokenAddr: '0x…::usdc::USDC', walletAddress: '0xw' })
    ).rejects.toThrow('You need SUI in this wallet to send USDC (network fee).');
  });
});
