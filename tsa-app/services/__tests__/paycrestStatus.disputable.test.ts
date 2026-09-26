import { isDisputableOrder, type PaycrestStatus } from '../paycrestStatus';

// Mirrors the server's paycrestPreMoneyStatuses (dispute_subjects.go): the
// button is only offered where the server would accept the report.
describe('isDisputableOrder', () => {
  it.each<[PaycrestStatus, boolean]>([
    ['awaiting_payment', false],
    ['awaiting_deposit', false],
    ['cancelled', false],
    ['pending', true],
    ['settling', true],
    ['settled', true],
    ['failed', true],
    ['expired', true],
    ['refunded', true],
    ['unmatched', true],
    ['converting', true],
    ['refunding', true],
    ['recovery_pending', true],
  ])('%s → %s', (status, want) => {
    expect(isDisputableOrder(status)).toBe(want);
  });
});
