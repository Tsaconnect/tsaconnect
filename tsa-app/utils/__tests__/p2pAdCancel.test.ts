import { adCancelFollowUp } from '../p2pAdCancel';

// babel-jest hoists this above the imports.
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));

describe('adCancelFollowUp (My Ads sell-ad cancel)', () => {
  it('confirmed / DB-only buy-ad cancel: toast and reload', () => {
    expect(adCancelFollowUp({ status: 'confirmed', txHash: '0x1', prepared: {} })).toEqual({
      toast: 'Ad cancelled', cancelSent: false, reload: true,
    });
    expect(adCancelFollowUp({ status: 'no-tx', prepared: {} }).toast).toBe('Ad cancelled');
  });
  it('confirm-failed: says it was sent and updates once recorded, holds the cancel, no immediate reload', () => {
    const f = adCancelFollowUp({ status: 'confirm-failed', txHash: '0xfeed', prepared: {}, message: 'x', mined: false });
    expect(f).toMatchObject({ cancelSent: true, reload: false });
    expect(f.alert?.message).toMatch(/Your cancel was sent\. The ad will update here once it’s recorded/);
    expect(f.alert?.message).toContain('0xfeed');
    expect(f.alert?.message).not.toMatch(/reconcil|automatically|on its own/i);
  });
  it('reverted: nothing changed — the shared copy, cancel offered again after a reload', () => {
    const f = adCancelFollowUp({ status: 'reverted', txHash: '0x1', prepared: {} });
    expect(f).toMatchObject({ cancelSent: false, reload: true });
    expect(f.alert?.title).toBe('Cancel failed');
  });
});
