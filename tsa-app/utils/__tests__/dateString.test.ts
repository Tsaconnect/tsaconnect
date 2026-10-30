import { addDays, fromDateString, toDateString } from '../dateString';

describe('dateString', () => {
  it('round-trips a local calendar day', () => {
    const d = fromDateString('2026-11-09');
    expect(d?.getFullYear()).toBe(2026);
    expect(d?.getMonth()).toBe(10);
    expect(d?.getDate()).toBe(9);
    expect(toDateString(d!)).toBe('2026-11-09');
  });

  it('rejects anything that is not YYYY-MM-DD', () => {
    expect(fromDateString('next friday')).toBeUndefined();
    expect(fromDateString('2026-1-9')).toBeUndefined();
    expect(fromDateString(undefined)).toBeUndefined();
  });

  it('adds days across a month end', () => {
    expect(addDays('2026-10-31', 1)).toBe('2026-11-01');
    expect(addDays('not a date', 1)).toBeUndefined();
    expect(addDays(undefined, 1)).toBeUndefined();
  });
});
