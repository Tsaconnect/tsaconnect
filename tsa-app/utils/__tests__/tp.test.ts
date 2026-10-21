import { formatTpUsd, rewardsLine, tpToUsd } from '../tp';

describe('tpToUsd', () => {
  it('values 1 TP at one cent', () => {
    expect(tpToUsd(1)).toBeCloseTo(0.01);
    expect(tpToUsd(1250)).toBeCloseTo(12.5);
  });
  it('is 0 for a non-finite amount', () => {
    expect(tpToUsd(NaN)).toBe(0);
    expect(tpToUsd(Infinity)).toBe(0);
  });
});

describe('formatTpUsd', () => {
  it('shows dollars and cents', () => {
    expect(formatTpUsd(1250)).toBe('$12.50');
    expect(formatTpUsd(0)).toBe('$0.00');
  });
  it('does not round a real balance down to nothing', () => {
    expect(formatTpUsd(0.07)).toBe('<$0.01');
  });
  it('groups thousands', () => {
    expect(formatTpUsd(1_000_000)).toBe('$10,000.00');
  });
});

describe('rewardsLine', () => {
  it('states cashback and TP from the live config', () => {
    expect(rewardsLine({ buyerCashbackPercent: 0.3, buyerTpPerUsd: 0.07 })).toBe(
      '0.3% cashback + 7 TP per $100 when you confirm delivery',
    );
  });
  it('drops whichever part the admin set to zero', () => {
    expect(rewardsLine({ buyerCashbackPercent: 0.3, buyerTpPerUsd: 0 })).toBe('0.3% cashback when you confirm delivery');
    expect(rewardsLine({ buyerCashbackPercent: 0, buyerTpPerUsd: 0.07 })).toBe('7 TP per $100 when you confirm delivery');
  });
  it('is undefined when there is nothing to promise', () => {
    expect(rewardsLine({ buyerCashbackPercent: 0, buyerTpPerUsd: 0 })).toBeUndefined();
    expect(rewardsLine(undefined)).toBeUndefined();
  });
});
