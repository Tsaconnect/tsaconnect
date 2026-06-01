import { classifyTradeFrame } from './tradeFrame';

const TRADE_ID = 'abc-123';

describe('classifyTradeFrame', () => {
  it('classifies a trade lifecycle event for this trade', () => {
    for (const type of [
      'p2p.trade.opened',
      'p2p.trade.paid',
      'p2p.trade.releasing',
      'p2p.trade.released',
      'p2p.trade.cancelled',
      'p2p.trade.disputed',
      'p2p.trade.resolved',
      'p2p.trade.expired',
    ]) {
      expect(classifyTradeFrame({ type, data: { tradeId: TRADE_ID } }, TRADE_ID)).toBe('trade');
    }
  });

  it('classifies a new chat message for this trade', () => {
    expect(
      classifyTradeFrame({ type: 'p2p.message.new', data: { tradeId: TRADE_ID, messageId: 'm1' } }, TRADE_ID),
    ).toBe('message');
  });

  it('ignores events for a different trade', () => {
    expect(classifyTradeFrame({ type: 'p2p.trade.paid', data: { tradeId: 'other' } }, TRADE_ID)).toBeNull();
    expect(classifyTradeFrame({ type: 'p2p.message.new', data: { tradeId: 'other' } }, TRADE_ID)).toBeNull();
  });

  it('ignores unrelated notification types', () => {
    expect(classifyTradeFrame({ type: 'p2p.ad.created', data: { tradeId: TRADE_ID } }, TRADE_ID)).toBeNull();
    expect(classifyTradeFrame({ type: 'kyc.approved', data: { tradeId: TRADE_ID } }, TRADE_ID)).toBeNull();
  });

  it('matches loosely when tradeId arrives as a number', () => {
    expect(classifyTradeFrame({ type: 'p2p.trade.paid', data: { tradeId: 123 } }, '123')).toBe('trade');
  });

  it('returns null on missing/empty input', () => {
    expect(classifyTradeFrame(null, TRADE_ID)).toBeNull();
    expect(classifyTradeFrame(undefined, TRADE_ID)).toBeNull();
    expect(classifyTradeFrame({ type: 'p2p.trade.paid' }, TRADE_ID)).toBeNull(); // no data
    expect(classifyTradeFrame({ type: 'p2p.trade.paid', data: { tradeId: TRADE_ID } }, '')).toBeNull(); // no tradeId
    expect(classifyTradeFrame({ data: { tradeId: TRADE_ID } }, TRADE_ID)).toBeNull(); // no type
  });
});
