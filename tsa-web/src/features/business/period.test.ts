import { monthToDate, periodProblem, watToday } from './period';

describe('periods in West Africa Time', () => {
  it("today is Lagos's date, an hour ahead of UTC", () => {
    expect(watToday(new Date('2026-10-07T22:59:00Z'))).toBe('2026-10-07');
    expect(watToday(new Date('2026-10-07T23:00:00Z'))).toBe('2026-10-08');
  });

  it('this month so far starts on the 1st, in Lagos', () => {
    expect(monthToDate(new Date('2026-10-08T09:00:00Z'))).toEqual({ from: '2026-10-01', to: '2026-10-08' });
    expect(monthToDate(new Date('2026-10-31T23:30:00Z'))).toEqual({ from: '2026-11-01', to: '2026-11-01' });
  });

  it('says what is wrong with a period', () => {
    expect(periodProblem('', '')).toBeNull();
    expect(periodProblem('2026-10-01', '2026-10-01')).toBeNull();
    expect(periodProblem('2026-10-01', '')).toBe('Choose both a start and an end date.');
    expect(periodProblem('', '2026-10-01')).toBe('Choose both a start and an end date.');
    expect(periodProblem('2026-02-30', '2026-03-01')).toBe('Enter dates as YYYY-MM-DD.');
    expect(periodProblem('2026-13-01', '2026-12-01')).toBe('Enter dates as YYYY-MM-DD.');
    expect(periodProblem('01/10/2026', '2026-10-31')).toBe('Enter dates as YYYY-MM-DD.');
    expect(periodProblem('2026-10-31', '2026-10-01')).toBe('The end date is before the start date.');
    expect(periodProblem('2024-01-01', '2024-12-31', 366)).toBeNull();
    expect(periodProblem('2024-01-01', '2025-01-01', 366)).toBe('Choose at most 366 days.');
  });
});
