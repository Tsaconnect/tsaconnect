import { openAmountRule } from './display';

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
