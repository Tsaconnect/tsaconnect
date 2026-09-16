// app/wallet/_layout.tsx
//
// Stack layout for the Wallet section (token details, manage, send, receive,
// instant pay, seed phrase, history, all tokens, import crypto) plus the MCGP
// and Instant Transfer sub-stacks.
//
// Header chrome comes from the shared useDefaultScreenOptions() so this section
// cannot drift from the rest of the app. Note there is deliberately no `index`
// route here: the wallet's home IS the tokenization tab, which is why every
// screen below is entered from outside this group and why the shared back
// chevron (and the WALLET_TAB fallback) matters so much here.
import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { WALLET_TAB } from '@/constants/routes';

const WalletLayout = () => {
  const screenOptions = useDefaultScreenOptions({ backFallback: WALLET_TAB });

  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="token" options={{ title: 'Token Details' }} />
      <Stack.Screen name="manage" options={{ title: 'Manage Wallet' }} />
      <Stack.Screen name="send" options={{ title: 'Send' }} />
      <Stack.Screen name="receive" options={{ title: 'Receive' }} />
      <Stack.Screen name="instant-pay" options={{ title: 'Instant Pay' }} />
      <Stack.Screen name="mcgp" options={{ headerShown: false }} />
      <Stack.Screen name="spot-trade" options={{ title: 'Spot Trade' }} />
      <Stack.Screen name="it" options={{ headerShown: false }} />
      <Stack.Screen name="paycrest" options={{ headerShown: false }} />
      <Stack.Screen name="seedphrase" options={{ title: 'Seed Phrase' }} />
      <Stack.Screen name="history" options={{ title: 'Transaction History' }} />
      <Stack.Screen name="tokens" options={{ title: 'All Tokens' }} />
      <Stack.Screen name="import-token" options={{ title: 'Import crypto' }} />
    </Stack>
  );
};

export default WalletLayout;
