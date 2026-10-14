import { useLocalSearchParams } from 'expo-router';
import SwapScreen from '@/screens/swap';

// The side is fixed per screen (the form has no flip), so it comes from the URL.
const Swap = () => {
  const { side } = useLocalSearchParams<{ side?: string }>();
  return <SwapScreen initialSide={side === 'sell' ? 'sell' : 'buy'} />;
};

export default Swap;
