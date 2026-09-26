const mockStore = new Map<string, string>();
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: {
    getItem: jest.fn(async (k: string) => mockStore.get(k) ?? null),
    setItem: jest.fn(async (k: string, v: string) => { mockStore.set(k, v); }),
    removeItem: jest.fn(async (k: string) => { mockStore.delete(k); }),
  },
}));

// eslint-disable-next-line import/first -- jest hoists the mock above.
import AsyncStorage from '@react-native-async-storage/async-storage';
// eslint-disable-next-line import/first
import { persistedCache } from '../persistedCache';

const isNumbers = (v: unknown): v is number[] => Array.isArray(v) && v.every(n => typeof n === 'number');

beforeEach(() => { mockStore.clear(); jest.useRealTimers(); });

it('reads back what was written, marked fresh within its TTL', async () => {
  const cache = persistedCache('k', 1000, isNumbers);
  await cache.write([1, 2]);
  expect(await cache.read()).toEqual({ value: [1, 2], fresh: true });
});

it('still serves a value past its TTL, marked stale', async () => {
  jest.useFakeTimers({ now: 0 });
  const cache = persistedCache('k', 1000, isNumbers);
  await cache.write([1]);
  jest.setSystemTime(1001);
  expect(await cache.read()).toEqual({ value: [1], fresh: false });
});

it('returns null when nothing is stored, after clear, or when the stored value fails validation', async () => {
  const cache = persistedCache('k', 1000, isNumbers);
  expect(await cache.read()).toBeNull();
  await cache.write([1]);
  await cache.clear();
  expect(await cache.read()).toBeNull();
  mockStore.set('k', JSON.stringify({ at: Date.now(), value: ['not a number'] }));
  expect(await cache.read()).toBeNull();
  mockStore.set('k', '{not json');
  expect(await cache.read()).toBeNull();
});

it('treats a storage failure as a miss rather than throwing', async () => {
  const cache = persistedCache('k', 1000, isNumbers);
  (AsyncStorage.getItem as jest.Mock).mockRejectedValueOnce(new Error('disk'));
  expect(await cache.read()).toBeNull();
  (AsyncStorage.setItem as jest.Mock).mockRejectedValueOnce(new Error('disk'));
  await expect(cache.write([1])).resolves.toBeUndefined();
});
