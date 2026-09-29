// hooks/useNameChangeEntry.ts
//
// Whether a verified user is offered "Change name": only while one can be
// started (the server gives no unavailableReason — switched off, or not
// verified, hides it) — or while she has a change open, which she can always
// finish. Anything the server can't answer hides it: the entry leads nowhere then.
import { useCallback, useState } from 'react';
import { useFocusEffect } from 'expo-router';
import { getNameChange, isTerminalNameChange } from '@/services/nameChangeApi';

export function useNameChangeEntry(verified: boolean): boolean {
  const [offered, setOffered] = useState(false);
  useFocusEffect(useCallback(() => {
    if (!verified) { setOffered(false); return undefined; }
    let cancelled = false;
    getNameChange()
      .then((o) => {
        const open = !!o.change && !isTerminalNameChange(o.change.status);
        if (!cancelled) setOffered(open || !o.unavailableReason);
      })
      .catch(() => { if (!cancelled) setOffered(false); });
    return () => { cancelled = true; };
  }, [verified]));
  return offered;
}
