// hooks/useFocusPolling.ts
//
// Run `poll` when the screen gains focus and every `everyMs` while it keeps it
// (and while `active`). Leaving the screen stops it: nothing polls from behind
// another screen. So does the app going to the background — a phone in a
// pocket makes no requests — and coming back to the foreground polls at once.
import { useCallback, useEffect, useState } from 'react';
import { AppState } from 'react-native';
import { useFocusEffect } from 'expo-router';

function useAppActive(): boolean {
  const [appActive, setAppActive] = useState(AppState.currentState !== 'background');
  useEffect(() => {
    const sub = AppState.addEventListener('change', (state) => setAppActive(state === 'active'));
    return () => sub.remove();
  }, []);
  return appActive;
}

export function useFocusPolling(poll: () => void, everyMs: number, active = true): void {
  const appActive = useAppActive();
  const running = active && appActive;
  useFocusEffect(
    useCallback(() => {
      if (!running) return undefined;
      poll();
      const t = setInterval(poll, everyMs);
      return () => clearInterval(t);
    }, [poll, everyMs, running]),
  );
}
