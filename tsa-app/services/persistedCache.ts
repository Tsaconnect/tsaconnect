// services/persistedCache.ts
//
// A value kept on the device across app launches, for data that is slow to
// fetch but changes rarely: serve what is stored at once, and let the caller
// refresh it in the background when `fresh` is false. A stale value is still
// returned — showing yesterday's list beats a spinner.
//
// Storage failures are misses, never errors: a cache must not break the screen.

import AsyncStorage from '@react-native-async-storage/async-storage';

export interface PersistedCache<T> {
  /** The stored value and whether it is within its TTL; null when nothing usable is stored. */
  read(): Promise<{ value: T; fresh: boolean } | null>;
  write(value: T): Promise<void>;
  clear(): Promise<void>;
}

export function persistedCache<T>(
  key: string,
  ttlMs: number,
  isValid: (value: unknown) => value is T,
): PersistedCache<T> {
  return {
    async read() {
      try {
        const raw = await AsyncStorage.getItem(key);
        if (!raw) return null;
        const entry = JSON.parse(raw);
        if (typeof entry?.at !== 'number' || !isValid(entry.value)) return null;
        return { value: entry.value, fresh: Date.now() - entry.at <= ttlMs };
      } catch {
        return null;
      }
    },
    async write(value) {
      try {
        await AsyncStorage.setItem(key, JSON.stringify({ at: Date.now(), value }));
      } catch {}
    },
    async clear() {
      try {
        await AsyncStorage.removeItem(key);
      } catch {}
    },
  };
}
