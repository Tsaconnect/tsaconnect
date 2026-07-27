// app/p2p/_layout.tsx
//
// Stack layout for the P2P trading subsection. Header chrome (background / tint
// / title color / font) is sourced from useTheme() tokens so it tracks light &
// dark automatically — mirrors the wallet/(productgroup) layouts and replaces
// the legacy hardcoded-white `defaultScreenOptions` constant. A persistent
// bottom nav (P2PTabBar) is rendered below the Stack — it stays mounted across
// P2P screens and self-hides on detail/flow screens.
import React from 'react';
import { View } from 'react-native';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';
import { P2PTabBar } from '../../components/p2p/P2PTabBar';
import { RAILS, railLabel } from '../../constants/rails';

const P2PLayout = () => {
  const { colors, type } = useTheme();

  const screenOptions: NativeStackNavigationOptions = {
    headerTitleAlign: 'center',
    headerTintColor: colors.accent,
    headerTitleStyle: {
      fontSize: type.title3.fontSize,
      fontFamily: fontFamily['600'],
      color: colors.textPrimary,
    },
    headerStyle: {
      backgroundColor: colors.surface,
    },
    headerShadowVisible: false,
    contentStyle: {
      backgroundColor: colors.background,
    },
  };

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
          <Stack.Screen name="pool" options={{ title: `${RAILS.p2p.name} Pool` }} />
          <Stack.Screen name="ad/[adId]" options={{ title: 'Ad Detail' }} />
          <Stack.Screen name="trade/[tradeId]" options={{ title: 'Trade' }} />
          <Stack.Screen name="dispute/[tradeId]" options={{ title: 'Open Dispute' }} />
          <Stack.Screen name="seller/[sellerId]" options={{ title: 'Merchant Profile' }} />
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
