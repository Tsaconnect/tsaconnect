import {
  STABLE_BUY_STATUSES,
  buyStatusLabel,
  buyStatusToHops,
  formatAssetUnits,
  formatMinorAsNaira,
  isAwaitingPayment,
  isBuyEstimate,
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
    //
    // `not.toBe('In progress')` is the part that actually enforces this:
    // buyStatusLabel falls back to 'In progress' for any string it doesn't
    // recognize, so a status added to STABLE_BUY_STATUSES without a LABELS
    // entry would otherwise still pass — it degrades gracefully at runtime,
    // but that means the length/non-empty checks alone don't catch a missing
    // mapping. This assertion is what makes the gap fail here instead.
    for (const s of STABLE_BUY_STATUSES) {
      const hops = buyStatusToHops(s);
      expect(hops.length).toBeGreaterThan(0);
      expect(buyStatusLabel(s)).not.toBe('');
      expect(buyStatusLabel(s)).not.toBe('In progress');
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

describe('isAwaitingPayment', () => {
  it('treats a pool buy still opening as unpaid, like pending_payment (I-5)', () => {
    expect(isAwaitingPayment('opening')).toBe(true);
    expect(isAwaitingPayment('pending_payment')).toBe(true);
  });

  it('is false once her money has landed, and on a cancelled order', () => {
    for (const s of ['payment_received', 'converting', 'delivering', 'settled', 'cancelled', 'failed', 'refunded', '']) {
      expect(isAwaitingPayment(s)).toBe(false);
    }
  });
});

describe('isBuyEstimate', () => {
  it('marks an opening buy as an estimate, like pending_payment outside its rate hold', () => {
    expect(isBuyEstimate('opening', false)).toBe(true);
    expect(isBuyEstimate('pending_payment', false)).toBe(true);
    expect(isBuyEstimate('pending_payment', true)).toBe(false);
    expect(isBuyEstimate('settled', false)).toBe(false);
  });
});

describe('isTerminalBuyStatus', () => {
  it('treats an operator refund as finished (spec §6)', () => {
    expect(isTerminalBuyStatus('refunded')).toBe(true);
  });

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

describe('buyStatusLabel', () => {
  it('names the review and refund states the way spec §6 words them', () => {
    expect(buyStatusLabel('buy_awaiting_rate')).toBe('Waiting for us to review');
    expect(buyStatusLabel('refunded')).toBe('Refunded');
  });

  it('shows no progress on a refunded purchase', () => {
    expect(buyStatusToHops('refunded').every((h) => h.state === 'pending')).toBe(true);
  });
});
