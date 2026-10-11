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

type InboxItem = { id?: unknown; data?: Record<string, unknown> | null };

/** The inbox list: realtime rows the REST feed doesn't have yet, then the
 *  feed, without the user's own actions. One pass over each list. */
export function mergeInbox<T extends InboxItem>(realtime: T[], fetched: T[], userId: string | null | undefined): T[] {
  const fetchedIds = new Set(fetched.map((n) => n.id));
  return [...realtime.filter((rt) => !fetchedIds.has(rt.id)), ...fetched].filter((n) => !isOwnAction(n, userId));
}
