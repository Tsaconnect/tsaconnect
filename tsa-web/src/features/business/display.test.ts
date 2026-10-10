import { chainLabel, chainLabelById, openAmountRule, txExplorerUrl } from './display';

const limits = { merchantMinAmount: 100_000, merchantMaxAmount: 500_000_000 };

// Mirrors the server's payOpenLinkBounds: the link's range narrowed by the business's limits now.
it('openAmountRule is the link range narrowed by the business limits', () => {
  expect(openAmountRule({ ...limits, minAmount: 0, maxAmount: 0 })).toBe('From ₦1,000.00 to ₦5,000,000.00');
  expect(openAmountRule({ ...limits, minAmount: 200_000, maxAmount: 0 })).toBe('From ₦2,000.00 to ₦5,000,000.00');
  expect(openAmountRule({ ...limits, minAmount: 0, maxAmount: 2_000_000 })).toBe('From ₦1,000.00 to ₦20,000.00');
  // The business's limits moved inside the link's own range: theirs win on both ends.
  expect(openAmountRule({ merchantMinAmount: 300_000, merchantMaxAmount: 1_000_000, minAmount: 200_000, maxAmount: 5_000_000 }))
    .toBe('From ₦3,000.00 to ₦10,000.00');
});

it('openAmountRule without the business limits (an older API) shows what the link has', () => {
  expect(openAmountRule({ merchantMinAmount: 0, merchantMaxAmount: 0, minAmount: 200_000, maxAmount: 0 })).toBe('From ₦2,000.00');
  expect(openAmountRule({ merchantMinAmount: 0, merchantMaxAmount: 0, minAmount: 0, maxAmount: 0 })).toBe('Any amount');
});

it('names a chain by key (as before) or by id', () => {
  expect(chainLabel('bsc')).toBe('BNB Chain');
  expect(chainLabel('newchain')).toBe('Newchain');
  expect(chainLabelById(8453)).toBe('Base');
  expect(chainLabelById(999)).toBe('chain 999');
});

// Review Focus 4.
it('links a real transaction hash to its chain\'s explorer, and nothing else', () => {
  const hash = `0x${'ab'.repeat(32)}`;
  expect(txExplorerUrl(hash, 56)).toBe(`https://bscscan.com/tx/${hash}`);
  expect(txExplorerUrl(hash, 8453)).toBe(`https://basescan.org/tx/${hash}`);
  expect(txExplorerUrl('../address/0x1111111111111111111111111111111111111111', 56)).toBeUndefined();
  expect(txExplorerUrl(`${hash}?a=1`, 56)).toBeUndefined();
  expect(txExplorerUrl(hash, 999)).toBeUndefined();
});
