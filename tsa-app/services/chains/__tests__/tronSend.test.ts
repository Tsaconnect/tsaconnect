import { signSecpDigest } from '../sign';
import { sendTron } from '../tronSend';

// minimal mocks so sendTron can run without real network / native modules
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('test-token'),
}));

jest.mock('../../wallet', () => ({
  getPrivateKey: jest.fn().mockResolvedValue(
    '0x0000000000000000000000000000000000000000000000000000000000000001'
  ),
}));

describe('sendTron', () => {
  it("throws 'prepare-tx returned no payloads' when prepared has no payloads array", async () => {
    global.fetch = jest.fn().mockResolvedValue({
      ok: true,
      json: jest.fn().mockResolvedValue({ data: { prepared: {} } }),
    }) as unknown as typeof fetch;

    await expect(
      sendTron({ from: 'T1', to: 'T2', amount: '1', symbol: 'TRX', walletAddress: 'T1' })
    ).rejects.toThrow('prepare-tx returned no payloads');
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
