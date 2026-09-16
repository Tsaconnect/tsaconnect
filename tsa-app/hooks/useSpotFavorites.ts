// hooks/useSpotFavorites.ts
//
// Device-local favorites for the spot catalogue (the ❤️ tab). Stored as an
// array of uppercase BASE SYMBOLS in AsyncStorage — favorites are a per-device
// preference, not server state.
//
// The v2 key is a deliberate reset: favorites used to be keyed by listing id,
// and rows are tokens now, so an id no longer identifies a row. Old entries are
// left behind rather than migrated — a stale id would silently favorite nothing.
import { useCallback, useEffect, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';

const STORAGE_KEY = 'tsa-spot-favorites-v2';

export function useSpotFavorites() {
  const [favoriteSymbols, setFavoriteSymbols] = useState<Set<string>>(new Set());

  useEffect(() => {
    let cancelled = false;
    AsyncStorage.getItem(STORAGE_KEY)
      .then((raw) => {
        if (cancelled || !raw) return;
        const parsed = JSON.parse(raw);
        if (Array.isArray(parsed)) {
          setFavoriteSymbols(
            new Set(
              parsed.filter((v): v is string => typeof v === 'string').map((v) => v.toUpperCase()),
            ),
          );
        }
      })
      .catch(() => {
        /* unreadable store → start empty */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const toggleFavorite = useCallback((symbol: string) => {
    const key = symbol.toUpperCase();
    setFavoriteSymbols((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      AsyncStorage.setItem(STORAGE_KEY, JSON.stringify([...next])).catch(() => {
        /* persistence is best-effort; in-memory state still updates */
      });
      return next;
    });
  }, []);

  return { favoriteSymbols, toggleFavorite };
}
