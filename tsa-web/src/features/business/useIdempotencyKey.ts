import { useRef } from 'react';

/**
 * One Idempotency-Key per request body: sending the same body again (a reply lost
 * on the way) reuses the key, so the API replays what it already made; any edit
 * is a new request with a new key, so it never meets the API's refusal of a key
 * reused with a different body. `forget` starts afresh after such a refusal.
 */
export function useIdempotencyKey() {
  const sent = useRef<{ body: string; key: string } | null>(null);
  return {
    keyFor(request: unknown) {
      const body = JSON.stringify(request);
      if (sent.current?.body !== body) sent.current = { body, key: crypto.randomUUID() };
      return sent.current.key;
    },
    forget() {
      sent.current = null;
    },
  };
}
