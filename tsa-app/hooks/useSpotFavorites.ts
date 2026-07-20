// hooks/useSpotFavorites.ts
//
// Device-local favorites for spot listings (the ❤️ tab). Stored as an id array
// in AsyncStorage — favorites are a per-device preference, not server state.
import { useCallback, useEffect, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';

const STORAGE_KEY = 'tsa-spot-favorites';

export function useSpotFavorites() {
  const [favoriteIds, setFavoriteIds] = useState<Set<string>>(new Set());

  useEffect(() => {
    let cancelled = false;
    AsyncStorage.getItem(STORAGE_KEY)
      .then((raw) => {
        if (cancelled || !raw) return;
        const parsed = JSON.parse(raw);
        if (Array.isArray(parsed)) setFavoriteIds(new Set(parsed.filter((v) => typeof v === 'string')));
      })
      .catch(() => {
        /* unreadable store → start empty */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const toggleFavorite = useCallback((id: string) => {
    setFavoriteIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      AsyncStorage.setItem(STORAGE_KEY, JSON.stringify([...next])).catch(() => {
        /* persistence is best-effort; in-memory state still updates */
      });
      return next;
    });
  }, []);

  return { favoriteIds, toggleFavorite };
}
