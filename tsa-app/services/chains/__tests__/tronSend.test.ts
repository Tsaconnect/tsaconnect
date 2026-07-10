import { signSecpDigest } from '../sign';
import { sendTron, prepareTron } from '../tronSend';

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

describe('sendTron', () => {
  it("throws 'prepare-tx returned no payloads' when prepared has no payloads array", async () => {
    mockFetchOnce([{ ok: true, body: { data: { prepared: {} } } }]);
    await expect(
      sendTron({ from: 'T1', to: 'T2', amount: '1', symbol: 'TRX', walletAddress: '0xw' })
    ).rejects.toThrow('prepare-tx returned no payloads');
  });

  it('prepares, signs with the Tron key, and broadcasts to a txHash', async () => {
    const prepared = {
      family: 'tron',
      raw: '{}',
      payloads: [{ index: 0, hashHex: 'a'.repeat(64), curve: 'secp256k1' }],
    };
    const fetchFn = mockFetchOnce([
      { ok: true, body: { data: { prepared, fee: { hasEnoughGas: true } } } },
      { ok: true, body: { data: { txHash: '0xabc123' } } },
    ]);

    const res = await sendTron({
      from: 'TXsender', to: 'TXrecipient', amount: '1000000', symbol: 'TRX', walletAddress: '0xw',
    });

    expect(res.txHash).toBe('0xabc123');
    // second call is submit-tx and carries a 65-byte signature
    const submitBody = JSON.parse((fetchFn.mock.calls[1][1] as any).body);
    expect(submitBody.sigs[0].sigHex).toMatch(/^[0-9a-f]{130}$/);
  });
});

describe('prepareTron gas preflight', () => {
  it('surfaces the backend note when the sender lacks gas (402)', async () => {
    mockFetchOnce([
      { ok: false, status: 402, body: { message: 'You need TRX in this wallet to send USDT.' } },
    ]);
    await expect(
      prepareTron({ from: 'T1', to: 'T2', amount: '1000000', symbol: 'USDT', tokenAddr: 'TR7...', walletAddress: '0xw' })
    ).rejects.toThrow('You need TRX in this wallet to send USDT.');
  });
});

describe('signSecpDigest', () => {
  it('produces a 65-byte hex signature (r||s||v)', () => {
    // known private key + digest; assert length/shape (Tron accepts recovery id v in {0,1})
    const pk = '0x0000000000000000000000000000000000000000000000000000000000000001';
    const digest = 'a'.repeat(64);
    const sig = signSecpDigest(digest, pk);
    expect(sig).toMatch(/^[0-9a-f]{130}$/); // 65 bytes = 130 hex chars
    const v = parseInt(sig.slice(128), 16);
    expect(v === 0 || v === 1).toBe(true);
  });
});
