// app/p2p/_layout.tsx
//
// Stack layout for the P2P trading subsection. Screens registered here
// share consistent navigation chrome via `defaultScreenOptions`.
import React from 'react';
import { Stack } from 'expo-router';
import { defaultScreenOptions } from '../../constants/navigation';

const P2PLayout = () => {
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="index" options={{ title: 'P2P Marketplace' }} />
      <Stack.Screen name="my-ads" options={{ title: 'My Ads' }} />
      <Stack.Screen name="my-trades" options={{ title: 'My Trades' }} />
      <Stack.Screen name="create-ad" options={{ title: 'Create Sell Ad' }} />
      <Stack.Screen name="pool" options={{ title: 'P2P Pool' }} />
      <Stack.Screen name="ad/[adId]" options={{ title: 'Ad Detail' }} />
      <Stack.Screen name="trade/[tradeId]" options={{ title: 'Trade' }} />
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
  );
};

export default P2PLayout;
