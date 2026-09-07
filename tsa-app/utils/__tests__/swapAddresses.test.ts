import { missingAddressNotice } from '../swapAddresses';

const ready = { chainName: 'BSC', address: '0xabc' };

describe('missingAddressNotice', () => {
  it('says nothing while both sides have an address', () => {
    expect(missingAddressNotice([ready, { chainName: 'BTC', address: 'bc1q…' }], true)).toBeNull();
  });

  it('promises an address only while one is actually being derived', () => {
    const notice = missingAddressNotice([ready, { chainName: 'BTC', address: null }], false);
    expect(notice).toMatch(/preparing your btc address/i);
  });

  // A key-only or watch-only wallet has no mnemonic, so its BTC address never
  // arrives. "Preparing…" there is a promise that never resolves and leaves the
  // user tapping a dead button.
  it('tells the user the key is missing once derivation has finished', () => {
    const notice = missingAddressNotice([ready, { chainName: 'BTC', address: null }], true);
    expect(notice).toMatch(/no btc key/i);
    expect(notice).not.toMatch(/preparing/i);
  });

  it('names the send side first when both are missing', () => {
    const notice = missingAddressNotice(
      [{ chainName: 'BTC', address: null }, { chainName: 'SOL', address: null }],
      true,
    );
    expect(notice).toMatch(/btc/i);
    expect(notice).not.toMatch(/sol/i);
  });
});
