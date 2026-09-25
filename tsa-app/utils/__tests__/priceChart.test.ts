import {
  chartFetchOutcome,
  formatChartTime,
  indexAtX,
  pctChange,
  priceDomain,
  priceFractionDigits,
} from '../priceChart';

describe('priceDomain', () => {
  it('pads a volatile series around its own range', () => {
    const { lo, hi } = priceDomain([100, 110, 120], { padRatio: 0.1 });
    expect(lo).toBeCloseTo(98);
    expect(hi).toBeCloseTo(122);
  });

  it('never zooms tighter than the minimum span, so peg noise stays flat', () => {
    // USDC wobbling by $0.0003 must not fill the chart like a crash.
    const { lo, hi } = priceDomain([0.9998, 1.0001, 0.99995], { minSpanRatio: 0.005, padRatio: 0 });
    expect(hi - lo).toBeCloseTo(0.005, 4);
    expect((hi + lo) / 2).toBeCloseTo(0.99995, 4);
  });

  it('survives a flat series', () => {
    const { lo, hi } = priceDomain([5, 5, 5]);
    expect(hi).toBeGreaterThan(lo);
  });
});

describe('indexAtX', () => {
  it('maps line x to the nearest point', () => {
    expect(indexAtX(0, 100, 5, 'line')).toBe(0);
    expect(indexAtX(24, 100, 5, 'line')).toBe(1);
    expect(indexAtX(100, 100, 5, 'line')).toBe(4);
  });

  it('maps candle x to the slot it falls in', () => {
    expect(indexAtX(0, 100, 4, 'candle')).toBe(0);
    expect(indexAtX(49, 100, 4, 'candle')).toBe(1);
    expect(indexAtX(99, 100, 4, 'candle')).toBe(3);
  });

  it('clamps a finger dragged past either edge', () => {
    expect(indexAtX(-30, 100, 5, 'line')).toBe(0);
    expect(indexAtX(500, 100, 5, 'candle')).toBe(4);
  });
});

describe('priceFractionDigits', () => {
  it.each([
    [4215.3, 2],
    [12.5, 4],
    [0.999851, 6],
    [0.35, 6],
    [0.000012, 8],
  ])('%p → %p digits', (price, digits) => {
    expect(priceFractionDigits(price)).toBe(digits);
  });
});

describe('formatChartTime', () => {
  const ts = new Date(2026, 8, 25, 9, 5).getTime(); // Fri 25 Sep 2026 09:05 local

  it('shows the clock for a day', () => {
    expect(formatChartTime(ts, 1)).toBe('09:05');
  });
  it('adds the weekday for a week', () => {
    expect(formatChartTime(ts, 7)).toBe('Fri 09:05');
  });
  it('shows the date for a month', () => {
    expect(formatChartTime(ts, 30)).toBe('25 Sep');
  });
  it('adds the year for a year', () => {
    expect(formatChartTime(ts, 365)).toBe('25 Sep 2026');
  });
});

describe('pctChange', () => {
  it('is signed percent from the first value', () => {
    expect(pctChange(100, 105)).toBeCloseTo(5);
    expect(pctChange(100, 90)).toBeCloseTo(-10);
  });
  it('is 0 when there is no base', () => {
    expect(pctChange(0, 5)).toBe(0);
  });
});

describe('chartFetchOutcome', () => {
  it('is ok with at least two points', () => {
    expect(chartFetchOutcome({ success: true, status: 200 }, 2)).toBe('ok');
  });
  it('is none when the server has no data for the asset', () => {
    expect(chartFetchOutcome({ success: false, status: 404 }, 0)).toBe('none');
    expect(chartFetchOutcome({ success: true, status: 200 }, 1)).toBe('none');
  });
  it('is failed when the server or network failed, so the user can retry', () => {
    expect(chartFetchOutcome({ success: false, status: 503 }, 0)).toBe('failed');
    expect(chartFetchOutcome({ success: false }, 0)).toBe('failed'); // request threw / non-JSON reply
  });
});
