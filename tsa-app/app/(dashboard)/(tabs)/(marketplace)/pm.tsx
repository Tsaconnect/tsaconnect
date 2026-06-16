// app/(dashboard)/(tabs)/(marketplace)/pm.tsx
//
// PM (Payment Merchant) tab — redirects immediately to the P2P marketplace
// with ?pm=1 so only PM ads (pm_id set) are shown. The filter is handled
// server-side by the /p2p/ads endpoint; the P2P screen header updates to
// "Payment Merchant — instant buy / sell" when it detects the pm param.
import { useEffect } from 'react';
import { router } from 'expo-router';

export default function PMScreen() {
  useEffect(() => {
    // Replace so the back-stack goes to the tab bar, not this redirect screen.
    router.replace('/p2p?pm=1');
  }, []);

  // Render nothing — the redirect fires on mount before the first paint.
  return null;
}
