// app/p2p/_layout.tsx
//
// Stack layout for the P2P trading subsection. Header chrome comes from the
// shared useDefaultScreenOptions(). A persistent bottom nav (P2PTabBar) is
// rendered below the Stack — it stays mounted across P2P screens and self-hides
// on detail/flow screens.
//
// `index` is entered from the Services hub, the Marketplace tab and the wallet's
// deposit/withdraw sheets, so it is route 0 of a freshly built stack every time.
// The shared headerLeft is what gives it a back chevron; before it, iOS users
// had no way out at all (Android had the BackHandler in index.tsx).
import React from 'react';
import { View } from 'react-native';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { SERVICES_TAB } from '@/constants/routes';
import { P2PTabBar } from '../../components/p2p/P2PTabBar';
import { railLabel } from '../../constants/rails';

const P2PLayout = () => {
  const screenOptions = useDefaultScreenOptions({ backFallback: SERVICES_TAB });

  return (
    <View style={{ flex: 1 }}>
      <View style={{ flex: 1 }}>
        <Stack screenOptions={screenOptions}>
          {/* index sets its own title — it renders both the Individual Payment
              feed and the Payment Merchant feed (?pm=1). */}
          <Stack.Screen name="index" options={{ title: railLabel('p2p') }} />
          <Stack.Screen name="my-ads" options={{ title: 'My Ads' }} />
          <Stack.Screen name="my-trades" options={{ title: 'My Trades' }} />
          <Stack.Screen name="create-ad" options={{ title: 'Create Sell Ad' }} />
          <Stack.Screen name="create-buy-ad" options={{ title: 'Post Buy Ad' }} />
          <Stack.Screen name="pool" options={{ title: 'Escrow funds' }} />
          <Stack.Screen name="ad/[adId]" options={{ title: 'Ad Detail' }} />
          <Stack.Screen name="trade/[tradeId]" options={{ title: 'Trade' }} />
          <Stack.Screen name="dispute/[tradeId]" options={{ title: 'Open Dispute' }} />
          <Stack.Screen name="seller/[sellerId]" options={{ title: 'Seller profile' }} />
          <Stack.Screen
            name="payment-methods/index"
            options={{ title: 'Payment Methods' }}
          />
          <Stack.Screen
            name="payment-methods/new"
            options={{ title: 'Add Payment Method' }}
          />
        </Stack>
      </View>
      <P2PTabBar />
    </View>
  );
};

export default P2PLayout;
