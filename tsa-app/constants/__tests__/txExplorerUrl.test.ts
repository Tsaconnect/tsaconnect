import { txExplorerUrl } from '@/constants/chains';

const EVM = `0x${'ab'.repeat(32)}`;
const HEX = 'cd'.repeat(32);

describe('txExplorerUrl', () => {
  it('builds a /tx/ link for a real hash on a known chain', () => {
    expect(txExplorerUrl(EVM, { chainKey: 'base' })).toBe(`https://basescan.org/tx/${EVM}`);
    expect(txExplorerUrl(EVM, { chainId: 56 })).toBe(`https://bscscan.com/tx/${EVM}`);
    expect(txExplorerUrl(HEX, { chainKey: 'btc' })).toMatch(new RegExp(`/tx/${HEX}$`));
    expect(txExplorerUrl(HEX, { chainKey: 'tron' })).toMatch(new RegExp(`/#/transaction/${HEX}$`));
  });

  it('never lets a hash steer the link elsewhere on the explorer', () => {
    // A provider-supplied "hash" like this would resolve to basescan.org/address/…
    expect(txExplorerUrl('../address/0x1111111111111111111111111111111111111111', { chainKey: 'base' })).toBeUndefined();
    expect(txExplorerUrl(`${EVM}?a=1`, { chainKey: 'base' })).toBeUndefined();
    expect(txExplorerUrl(`${EVM}#x`, { chainKey: 'base' })).toBeUndefined();
    expect(txExplorerUrl(`../${HEX}`, { chainKey: 'btc' })).toBeUndefined();
    expect(txExplorerUrl(`${HEX}/..`, { chainKey: 'tron' })).toBeUndefined();
  });

  it('refuses what is not a hash, or a chain it does not know', () => {
    expect(txExplorerUrl('0x1234', { chainKey: 'base' })).toBeUndefined();
    expect(txExplorerUrl('', { chainKey: 'base' })).toBeUndefined();
    expect(txExplorerUrl(EVM, { chainKey: 'nochain' as never })).toBeUndefined();
  });
});
