import { isChatFrameFor } from '@/utils/chatFrame';

describe('isChatFrameFor', () => {
  it('matches a new message in this conversation only', () => {
    expect(isChatFrameFor({ type: 'chat.message.new', data: { threadId: 't1' } }, 't1')).toBe(true);
    expect(isChatFrameFor({ type: 'chat.message.new', data: { threadId: 't2' } }, 't1')).toBe(false);
    expect(isChatFrameFor({ type: 'p2p.message.new', data: { threadId: 't1' } }, 't1')).toBe(false);
    expect(isChatFrameFor({ type: 'chat.message.new', data: null }, 't1')).toBe(false);
    expect(isChatFrameFor(null, 't1')).toBe(false);
  });
});
