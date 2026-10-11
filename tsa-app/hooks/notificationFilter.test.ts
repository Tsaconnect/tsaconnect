import { isOwnAction } from './notificationFilter';

describe('isOwnAction', () => {
  it('true when the event’s actor is the signed-in user', () => {
    expect(isOwnAction({ data: { actorId: 'u1', tradeId: 't' } }, 'u1')).toBe(true);
  });
  it('false for the counterparty, untagged frames, or no user', () => {
    expect(isOwnAction({ data: { actorId: 'u2' } }, 'u1')).toBe(false);
    expect(isOwnAction({ data: { tradeId: 't' } }, 'u1')).toBe(false);
    expect(isOwnAction({ data: { actorId: 'u1' } }, undefined)).toBe(false);
    expect(isOwnAction(null, 'u1')).toBe(false);
    expect(isOwnAction({ data: null }, 'u1')).toBe(false);
  });
});
