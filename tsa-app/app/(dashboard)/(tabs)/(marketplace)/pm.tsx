// app/(dashboard)/(tabs)/(marketplace)/pm.tsx
//
// PM (Payment Merchant) tab — redirects immediately to the P2P marketplace
// with ?pm=1 so only PM ads (pm_id set) are shown. The filter is handled
// server-side by the /p2p/ads endpoint; the P2P screen header updates to
// "Payment Merchant — instant buy / sell" when it detects the pm param.
//
// <Redirect> rather than a router.replace() in an effect: it resolves during
// render, so the route never mounts as a screen at all — no blank frame, and
// nothing for the back stack to land on.
import { Redirect } from 'expo-router';

export default function PMScreen() {
  return <Redirect href="/p2p?pm=1" />;
}
