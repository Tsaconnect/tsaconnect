// utils/back.ts
//
// The app's ONE back action.
//
// `router.back()` alone is not enough. Two situations make an unguarded call
// wrong:
//
//   1. Cold entry — a push notification, a deep link, or an OAuth/KYC callback
//      can make any screen the first route in the app. `router.back()` is then
//      a silent no-op and the user is trapped.
//   2. Group roots — a screen entered from another group sits at index 0 of its
//      own nested stack (see constants/navigation.tsx). `router.back()` still
//      works there (it unwinds to the caller through the root stack), but the
//      native chevron is absent, so the caller has to supply its own control.
//
// Every back control in the app — the native header chevron, ScreenHeader, and
// any hand-rolled row — routes through here so the three cases can never drift.
import { router, type Href } from 'expo-router';

/**
 * Pop one screen, or land on `fallback` when there is no history to pop.
 *
 * `fallback` should be the SECTION the screen belongs to (the wallet tab for a
 * wallet screen, the services tab for bills), never a blanket `/home` — sending
 * a user to the app root because they arrived from a notification is the exact
 * "back takes me to main" behaviour this module exists to prevent.
 */
export function goBack(fallback: Href): void {
  if (router.canGoBack()) {
    router.back();
    return;
  }
  // No history at all: `replace` rather than `push` so the fallback becomes the
  // root of a fresh stack instead of stacking on top of a dead-end screen.
  router.replace(fallback);
}

/** True when a back control has somewhere to go. */
export function hasHistory(): boolean {
  return router.canGoBack();
}
