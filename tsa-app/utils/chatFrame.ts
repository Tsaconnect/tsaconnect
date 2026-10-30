// Pure classifier for inbound realtime frames (no network or storage imports,
// so it is unit-testable): is this a new message in the given conversation?
export function isChatFrameFor(
  frame: { type?: string; data?: Record<string, unknown> | null } | null | undefined,
  threadId: string,
): boolean {
  return !!frame && frame.type === 'chat.message.new' && String(frame.data?.threadId ?? '') === threadId;
}
