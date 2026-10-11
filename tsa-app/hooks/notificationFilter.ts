// A frame caused by the signed-in user is not news to them: keep it out of the
// inbox/badge. The API tags such frames with data.actorId; untagged frames
// (system/reconciler, non-P2P) always show.
export function isOwnAction(
  n: { data?: Record<string, unknown> | null } | null | undefined,
  userId: string | null | undefined,
): boolean {
  const actor = n?.data?.actorId;
  return !!userId && actor != null && String(actor) === String(userId);
}
