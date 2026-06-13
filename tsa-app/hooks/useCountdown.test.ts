import { formatCountdown, secondsLeft } from './useCountdown';

describe('countdown helpers', () => {
  it('secondsLeft clamps to 0 when past', () => {
    const now = new Date('2026-05-28T12:00:00Z').getTime();
    expect(secondsLeft('2026-05-28T12:00:30Z', now)).toBe(30);
    expect(secondsLeft('2026-05-28T11:59:00Z', now)).toBe(0);
  });

  it('formatCountdown renders mm:ss', () => {
    expect(formatCountdown(0)).toBe('0:00');
    expect(formatCountdown(65)).toBe('1:05');
    expect(formatCountdown(600)).toBe('10:00');
  });
});
