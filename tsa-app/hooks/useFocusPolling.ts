// hooks/useFocusPolling.ts
//
// Run `poll` when the screen gains focus and every `everyMs` while it keeps it
// (and while `active`). Leaving the screen stops it: nothing polls from behind
// another screen.
import { useCallback } from 'react';
import { useFocusEffect } from 'expo-router';

export function useFocusPolling(poll: () => void, everyMs: number, active = true): void {
  useFocusEffect(
    useCallback(() => {
      if (!active) return undefined;
      poll();
      const t = setInterval(poll, everyMs);
      return () => clearInterval(t);
    }, [poll, everyMs, active]),
  );
}
