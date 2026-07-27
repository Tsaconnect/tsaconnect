// utils/__tests__/itAnchor.test.ts
//
// The wallet's Deposit and Withdraw sheets deep-link straight to
// /wallet/it/buy and /wallet/it/sell, skipping the IT hub. expo-router builds a nested stack's
// routes array from the URL alone, so without an anchor the IT stack becomes
// routes: [buy] at index 0 — and native-stack renders a working header back
// button only when a route precedes the current one in that same navigator.
// The anchor makes getStateFromPath insert `index` beneath the target
// (createStateObject → { index: 1, routes: [initialRoute, route] }).
//
// Guarding the setting rather than the rendered header: the bug lives in route
// configuration, and mounting a native stack in jest would test React
// Navigation, not us.

/* eslint-disable import/first -- the mock is written above the imports it
   affects; jest hoists it either way, but the order documents the dependency. */

// Importing the layout pulls in the theme provider, which reaches for
// AsyncStorage at module scope. Mocked per-file (jest hoists this above the
// imports below) rather than in a global setup file.
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

import { unstable_settings } from '@/app/wallet/it/_layout';
import { RAIL_ROUTES } from '@/constants/rails';

describe('IT stack anchor', () => {
  it('anchors the IT stack to the hub so deep-linked screens have a back route', () => {
    expect(unstable_settings?.initialRouteName).toBe('index');
  });

  it('is required because the wallet sheets link past the hub', () => {
    // If these ever stop pointing below /wallet/it, revisit the anchor above.
    expect([RAIL_ROUTES.itBuy, RAIL_ROUTES.itSell]).toEqual([
      '/wallet/it/buy',
      '/wallet/it/sell',
    ]);
  });
});
