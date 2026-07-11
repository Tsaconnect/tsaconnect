import { sendBtc, prepareBtc } from '../btcSend';

// minimal mocks so the flow can run without real network / native modules
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('test-token'),
}));

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
jest.mock('../../wallet', () => ({
  getMnemonic: jest.fn().mockResolvedValue(M),
}));

function mockFetchOnce(responses: { ok: boolean; status?: number; body: any }[]) {
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

describe('sendBtc', () => {
  it("throws 'prepare-tx returned no payloads' when prepared has no payloads array", async () => {
    mockFetchOnce([{ ok: true, body: { data: { prepared: {} } } }]);
    await expect(
      sendBtc({ from: 'bc1qsender', to: 'bc1qrecipient', amount: '1000', symbol: 'BTC', walletAddress: '0xw' })
    ).rejects.toThrow('prepare-tx returned no payloads');
  });

  it('signs EVERY input payload with the BIP-84 key and broadcasts to a txHash', async () => {
    // Two payloads — a multi-UTXO spend must produce one signature per input.
    const prepared = {
      family: 'btc',
      raw: '{}',
      payloads: [
        { index: 0, hashHex: 'a'.repeat(64), curve: 'secp256k1' },
        { index: 1, hashHex: 'b'.repeat(64), curve: 'secp256k1' },
      ],
    };
    const fetchFn = mockFetchOnce([
      { ok: true, body: { data: { prepared, fee: { hasEnoughGas: true } } } },
      { ok: true, body: { data: { txHash: 'cafebabe' } } },
    ]);

    const res = await sendBtc({
      from: 'bc1qsender', to: 'bc1qrecipient', amount: '80000', symbol: 'BTC', walletAddress: '0xw',
    });

    expect(res.txHash).toBe('cafebabe');
    // prepare call targets the btc chain
    expect(String(fetchFn.mock.calls[0][0])).toContain('chain=btc');
    // second call is submit-tx with one 65-byte r||s||v signature per input
    const submitBody = JSON.parse((fetchFn.mock.calls[1][1] as any).body);
    expect(submitBody.sigs).toHaveLength(2);
    for (const [i, sig] of submitBody.sigs.entries()) {
      expect(sig.index).toBe(i);
      expect(sig.sigHex).toMatch(/^[0-9a-f]{130}$/);
    }
    // distinct digests must yield distinct signatures
    expect(submitBody.sigs[0].sigHex).not.toBe(submitBody.sigs[1].sigHex);
  });
});

describe('prepareBtc fee preflight', () => {
  it('surfaces the backend note when the balance cannot fund the send (402)', async () => {
    mockFetchOnce([
      { ok: false, status: 402, body: { message: 'Not enough BTC to cover the amount plus the network fee.' } },
    ]);
    await expect(
      prepareBtc({ from: 'bc1qs', to: 'bc1qr', amount: '80000', symbol: 'BTC', walletAddress: '0xw' })
    ).rejects.toThrow('Not enough BTC to cover the amount plus the network fee.');
  });
});
