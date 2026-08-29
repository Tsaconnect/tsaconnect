// app/(detail)/otc-trade.tsx
//
// The OTC desk's trade form, reached by tapping Buy or Sell on a listing row.
// PM's term for it is the "internal page" — the step a paused direction must
// not reach.
//
// It lives in (detail) rather than inside the Easyswap tab so it pushes onto
// the root stack: back returns to the listing the user came from, and the
// screen gets the shared header chevron. See app/(detail)/_layout.tsx.
import { useLocalSearchParams } from 'expo-router';
import SwapScreen from '@/screens/swap';

export default function OtcTrade() {
  const { side } = useLocalSearchParams<{ side?: string }>();
  return <SwapScreen initialSide={side === 'sell' ? 'sell' : 'buy'} />;
}
