import React from 'react';
import { Stack } from 'expo-router';
import { defaultScreenOptions } from '../../constants/navigation';

const WalletLayout = () => {
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="home" options={{ headerShown: false }} />
      <Stack.Screen name="manage" options={{ title: 'Manage Wallet' }} />
      <Stack.Screen name="send" options={{ title: 'Send' }} />
      <Stack.Screen name="receive" options={{ title: 'Receive' }} />
      <Stack.Screen name="seedphrase" options={{ title: 'Seed Phrase' }} />
    </Stack>
  );
};

export default WalletLayout;
