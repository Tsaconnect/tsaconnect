/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The link screens' polling. What this pins: it polls on focus and every
 * interval after; a background app polls nothing; coming back polls at once.
 */
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  // Focus once, on mount — what a first visit to the screen does.
  return { __esModule: true, useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]) };
});

import React from 'react';
import { AppState, type AppStateStatus } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { useFocusPolling } from '../useFocusPolling';

let onChange: (s: AppStateStatus) => void = () => {};
let tree: ReactTestRenderer;
const poll = jest.fn();

function Screen() {
  useFocusPolling(poll, 15_000);
  return null;
}

beforeEach(() => {
  jest.useFakeTimers();
  poll.mockClear();
  jest.spyOn(AppState, 'addEventListener').mockImplementation(((_: string, cb: (s: AppStateStatus) => void) => {
    onChange = cb;
    return { remove: jest.fn() };
  }) as never);
});
afterEach(() => { act(() => tree.unmount()); jest.useRealTimers(); });

it('polls on focus and every interval while the app is in front', () => {
  act(() => { tree = TestRenderer.create(<Screen />); });
  expect(poll).toHaveBeenCalledTimes(1);
  act(() => { jest.advanceTimersByTime(30_000); });
  expect(poll).toHaveBeenCalledTimes(3);
});

it('stops in the background, and polls at once on coming back', () => {
  act(() => { tree = TestRenderer.create(<Screen />); });
  act(() => { onChange('background'); });
  poll.mockClear();
  act(() => { jest.advanceTimersByTime(60_000); });
  expect(poll).not.toHaveBeenCalled();
  act(() => { onChange('active'); });
  expect(poll).toHaveBeenCalledTimes(1);
});
