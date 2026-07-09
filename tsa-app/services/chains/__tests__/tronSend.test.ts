import { signSecpDigest } from '../sign';

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
