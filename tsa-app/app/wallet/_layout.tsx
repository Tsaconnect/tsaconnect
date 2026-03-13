import React from 'react';
import { Stack } from 'expo-router';

const WalletLayout = () => {
  return (
    <Stack screenOptions={{ headerShown: false }}>
      <Stack.Screen name="home" />
      <Stack.Screen name="manage" />
      <Stack.Screen name="send" />
      <Stack.Screen name="receive" />
      <Stack.Screen name="seedphrase" />
    </Stack>
  );
};

export default WalletLayout;
