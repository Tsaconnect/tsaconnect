import {
  STABLE_BUY_STATUSES,
  buyStatusLabel,
  buyStatusToHops,
  formatAssetUnits,
  formatMinorAsNaira,
  isTerminalBuyStatus,
} from '../stableBuyQuote';

describe('formatAssetUnits', () => {
  it('converts 6-decimal base units to display', () => {
    // 18 decimals would be wrong here by 10^12 — sell.tsx hardcodes 18.
    expect(formatAssetUnits('98450000')).toBe('98.45');
    expect(formatAssetUnits('100000000')).toBe('100');
    expect(formatAssetUnits('1')).toBe('0.000001');
    expect(formatAssetUnits('0')).toBe('0');
  });

  it('is defensive about unusable input', () => {
    expect(formatAssetUnits('')).toBe('0');
    expect(formatAssetUnits('abc')).toBe('0');
  });
});

describe('formatMinorAsNaira', () => {
  it('groups thousands and always shows two decimals', () => {
    expect(formatMinorAsNaira('16502500')).toBe('165,025.00');
    expect(formatMinorAsNaira('100')).toBe('1.00');
    expect(formatMinorAsNaira('5')).toBe('0.05');
  });

  it('is defensive about unusable input', () => {
    expect(formatMinorAsNaira('')).toBe('0.00');
    expect(formatMinorAsNaira('abc')).toBe('0.00');
  });
});

describe('buyStatusToHops', () => {
  it('maps EVERY backend buy status', () => {
    // An unmapped status renders a blank progress screen. buy_awaiting_rate
    // is the worst case: the user's naira is held pending an operator.
    for (const s of STABLE_BUY_STATUSES) {
      const hops = buyStatusToHops(s);
      expect(hops.length).toBeGreaterThan(0);
      expect(buyStatusLabel(s)).not.toBe('');
    }
  });

  it('advances as the purchase progresses', () => {
    const paid = buyStatusToHops('payment_received');
    const delivering = buyStatusToHops('delivering');
    const doneCount = (hs: ReturnType<typeof buyStatusToHops>) =>
      hs.filter((h) => h.state === 'done').length;
    expect(doneCount(delivering)).toBeGreaterThan(doneCount(paid));
  });

  it('marks every hop done when settled', () => {
    expect(buyStatusToHops('settled').every((h) => h.state === 'done')).toBe(true);
  });

  it('flags the parked state as failed so it reads as needing attention', () => {
    expect(buyStatusToHops('buy_awaiting_rate').some((h) => h.state === 'failed')).toBe(true);
  });
});

describe('isTerminalBuyStatus', () => {
  it('treats only genuinely finished states as terminal', () => {
    expect(isTerminalBuyStatus('settled')).toBe(true);
    expect(isTerminalBuyStatus('cancelled')).toBe(true);
    expect(isTerminalBuyStatus('failed')).toBe(true);
  });

  it('does NOT treat the parked state as terminal', () => {
    // An operator can reprice it back to payment_received. Stopping the poll
    // would freeze the screen after they did.
    expect(isTerminalBuyStatus('buy_awaiting_rate')).toBe(false);
    expect(isTerminalBuyStatus('converting')).toBe(false);
  });
});
