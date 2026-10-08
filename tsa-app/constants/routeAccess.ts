// constants/routeAccess.ts
//
// Which root-stack routes a signed-out user may see. Everything else sits
// behind <Stack.Protected guard={signedIn}> in app/_layout.tsx: when the
// session ends, expo-router drops those screens from history and falls back to
// `index`, which sends the user to /login.
//
// Every root route must appear in exactly one list —
// app/__tests__/routeAccess.test.ts fails on a new route that isn't
// classified, so a screen can't become reachable signed-out by accident.

/** Reachable without a session: sign-in/up, recovery, legal pages, and pay links (they show their own sign-in state). */
export const PUBLIC_ROUTES = [
  'index',
  'login',
  'signup',
  'verify',
  'passwordOTP',
  'recovery',
  'legal',
  'pay',
  '+not-found',
] as const;

export const PROTECTED_ROUTES = [
  '(dashboard)',
  '(detail)',
  '(productgroup)',
  '(servicegroup)',
  'activity',
  'add-category',
  'adverts',
  'bills',
  'category',
  'delete-account',
  'kyc/callback',
  'liquidity/index',
  'lock',
  'merchants',
  'order',
  'orderdetail',
  'p2p',
  'payments',
  'pin-setup',
  'profile',
  'reward-program',
  'wallet',
] as const;
