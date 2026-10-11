import { isOwnAction, mergeInbox } from '../notificationFilter';

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

describe('mergeInbox', () => {
  it('realtime rows not yet in the feed first, then the feed, minus the user\'s own actions', () => {
    const rt = [{ id: 'n3' }, { id: 'n2', data: { x: 1 } }, { id: 'n4', data: { actorId: 'u1' } }];
    const feed = [{ id: 'n2' }, { id: 'n1' }, { id: 'n0', data: { actorId: 'u1' } }];
    expect(mergeInbox(rt, feed, 'u1').map((n) => n.id)).toEqual(['n3', 'n2', 'n1']);
  });
  it('keeps everything when signed out', () => {
    expect(mergeInbox([{ id: 'a', data: { actorId: 'u1' } }], [], undefined)).toHaveLength(1);
  });
});
