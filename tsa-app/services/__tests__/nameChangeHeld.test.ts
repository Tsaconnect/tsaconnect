/**
 * Pacing the name-change payment confirm: the screen polls every few seconds,
 * but the confirm route allows 60 calls in 15 minutes — so the hash is asked
 * about at most once every NAME_CHANGE_CONFIRM_EVERY_MS, and a confirm made
 * another way (a send, "Check now") restarts the wait.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

// eslint-disable-next-line import/first -- jest hoists the mock above the import it replaces.
import { NAME_CHANGE_CONFIRM_EVERY_MS, confirmPacer } from '../nameChangeHeld';

it('grants a confirm at most once per interval, and counts one made another way', () => {
  let now = 1_000_000;
  const pacer = confirmPacer(() => now);
  expect(pacer.claim()).toBe(true);
  expect(pacer.claim()).toBe(false);
  now += NAME_CHANGE_CONFIRM_EVERY_MS - 1;
  expect(pacer.claim()).toBe(false);
  now += 1;
  expect(pacer.claim()).toBe(true);
  pacer.mark();
  now += NAME_CHANGE_CONFIRM_EVERY_MS - 1;
  expect(pacer.claim()).toBe(false);
  // Polled every 5 s for 15 minutes: well under the route's 60.
  let granted = 0;
  const fresh = confirmPacer(() => now);
  for (let t = 0; t < 15 * 60_000; t += 5_000) { now += 5_000; if (fresh.claim()) granted++; }
  expect(granted).toBeLessThanOrEqual(30);
});
