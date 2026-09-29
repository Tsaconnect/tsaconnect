/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * "Change name" is offered to a verified user only while a change can be
 * started — or while one is open, which she can always finish. Switched off,
 * not verified, or unanswerable: hidden (O3).
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return { __esModule: true, useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]) };
});
jest.mock('@/services/nameChangeApi', () => ({ ...jest.requireActual('@/services/nameChangeApi'), getNameChange: jest.fn() }));

import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';
import { getNameChange, type NameChangeOverview } from '@/services/nameChangeApi';
import { useNameChangeEntry } from '../useNameChangeEntry';

const limits = { maxPerYear: 2, completedInLastYear: 0, maxAttempts: 3, ttlDays: 7 };
const base: NameChangeOverview = { enabled: true, change: null, eligible: true, blockers: [], limits, quote: null };

async function offered(verified: boolean): Promise<boolean> {
  let seen = false;
  function Probe() { seen = useNameChangeEntry(verified); return null; }
  await act(async () => { TestRenderer.create(<Probe />); });
  await act(async () => { await new Promise((r) => setImmediate(r)); });
  return seen;
}

beforeEach(() => jest.clearAllMocks());

it('offered while a change can be started', async () => {
  jest.mocked(getNameChange).mockResolvedValue(base);
  await expect(offered(true)).resolves.toBe(true);
});

it('hidden while name changes are switched off, or she is not verified', async () => {
  jest.mocked(getNameChange).mockResolvedValue({ ...base, enabled: false, unavailableReason: 'disabled' });
  await expect(offered(true)).resolves.toBe(false);
  jest.mocked(getNameChange).mockResolvedValue({ ...base, unavailableReason: 'not_verified' });
  await expect(offered(true)).resolves.toBe(false);
});

it('offered while a change is open, even switched off — she can always finish it', async () => {
  jest.mocked(getNameChange).mockResolvedValue({
    ...base, enabled: false, unavailableReason: 'disabled',
    change: { id: 'nc1', status: 'paid' } as NameChangeOverview['change'],
  });
  await expect(offered(true)).resolves.toBe(true);
});

it('hidden when the server cannot answer, and never asked for an unverified user', async () => {
  jest.mocked(getNameChange).mockRejectedValue(new Error('offline'));
  await expect(offered(true)).resolves.toBe(false);
  jest.mocked(getNameChange).mockClear();
  await expect(offered(false)).resolves.toBe(false);
  expect(getNameChange).not.toHaveBeenCalled();
});
