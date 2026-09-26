import { naira, dateTime, relative, initials, parseNaira } from './format';

it('naira formats kobo with two decimals and grouping', () => {
  expect(naira(8_450_000)).toBe('₦84,500.00');
  expect(naira(15)).toBe('₦0.15');
  expect(naira(123_456_789_00)).toBe('₦123,456,789.00');
});

it('dateTime is short and local, with no ICU month quirks', () => {
  expect(dateTime('2026-09-24T16:02:00Z')).toBe('24 Sep, 16:02');
  expect(dateTime('2026-01-05T09:07:00Z')).toBe('5 Jan, 09:07');
});

it('relative reads like the mockup', () => {
  const now = new Date('2026-09-24T16:10:00Z');
  expect(relative('2026-09-24T16:09:50Z', now)).toBe('just now');
  expect(relative('2026-09-24T16:08:00Z', now)).toBe('2 min ago');
  expect(relative('2026-09-24T13:10:00Z', now)).toBe('3 h ago');
  expect(relative('2026-09-20T10:00:00Z', now)).toBe('20 Sep');
});

it('initials come from the first two words', () => {
  expect(initials('Ada Obi')).toBe('AO');
  expect(initials('Sky Air Ltd')).toBe('SA');
  expect(initials('  ada ')).toBe('A');
  expect(initials('')).toBe('?');
});

it('parseNaira reads what a merchant types as naira, in kobo', () => {
  expect(parseNaira('5000')).toBe(500_000);
  expect(parseNaira('5,000.5')).toBe(500_050);
  expect(parseNaira(' ₦84,500.00 ')).toBe(8_450_000);
  expect(parseNaira('0.01')).toBe(1);
  for (const bad of ['', '0', '0.00', '-5', '5.001', '5e3', 'abc', '1.2.3']) expect(parseNaira(bad)).toBeNull();
});
