// Tests for the pure greeting helper backing the IT trading-agreement gate.

import { agreementGreeting } from '../agreement-copy';

describe('agreementGreeting', () => {
  it('greets by first name only', () => {
    expect(agreementGreeting('Ada Lovelace')).toBe('Hello Ada,');
    expect(agreementGreeting('Madonna')).toBe('Hello Madonna,');
  });

  it('trims and collapses whitespace', () => {
    expect(agreementGreeting('  Ada   Lovelace ')).toBe('Hello Ada,');
  });

  it('falls back to a plain greeting when the name is missing', () => {
    expect(agreementGreeting('')).toBe('Hello,');
    expect(agreementGreeting('   ')).toBe('Hello,');
    expect(agreementGreeting(null)).toBe('Hello,');
    expect(agreementGreeting(undefined)).toBe('Hello,');
  });
});
