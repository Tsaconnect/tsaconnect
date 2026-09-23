// utils/paymentId.ts
//
// The anti-injection gate for a TSA Pay payment id: a payment id is always a
// server-generated UUID, and a pay link's `id` param is attacker-controlled
// (a deep link, a pasted URL, a value round-tripped through AsyncStorage), so
// every entry point that turns one into a request path, a stored value, or a
// route checks this first — see services/payApi.ts (getCheckout,
// submitTestOutcome), utils/postAuthRoute.ts, and utils/payCheckout.ts.
//
// Deliberately zero-dependency: services/payApi.ts's request() chain pulls in
// walletApi.ts's top-level `import AsyncStorage from
// '@react-native-async-storage/async-storage'`, which throws outside a
// mocked test — and utils/payCheckout.ts is a pure function that must stay
// importable (and testable) without that chain. Kept here as the single
// source of truth so the two never drift apart; payApi.ts re-exports it
// rather than redeclaring it.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isPaymentId(id: unknown): id is string {
  return typeof id === 'string' && UUID_RE.test(id);
}
