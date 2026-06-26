// app/wallet/_layout.tsx
//
// Stack layout for the Wallet section (home, token details, manage, send,
// receive, instant pay, seed phrase, history, all tokens, import crypto).
// Migrated onto the TSA Signature design system: header chrome (background /
// tint / title color / font) is now sourced from useTheme() tokens instead of
// the legacy `defaultScreenOptions` constant, so it tracks light & dark
// automatically. Screen registrations, names, order, and titles are preserved
// verbatim — `home` still hides its header (`headerShown: false`) exactly as
// before; the themed options apply to every screen that renders a header.
import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

const WalletLayout = () => {
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
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="home" options={{ headerShown: false }} />
      <Stack.Screen name="token" options={{ title: 'Token Details' }} />
      <Stack.Screen name="manage" options={{ title: 'Manage Wallet' }} />
      <Stack.Screen name="send" options={{ title: 'Send' }} />
      <Stack.Screen name="receive" options={{ title: 'Receive' }} />
      <Stack.Screen name="instant-pay" options={{ title: 'Instant Pay' }} />
      <Stack.Screen name="mcgp" options={{ headerShown: false }} />
      <Stack.Screen name="it" options={{ headerShown: false }} />
      <Stack.Screen name="seedphrase" options={{ title: 'Seed Phrase' }} />
      <Stack.Screen name="history" options={{ title: 'Transaction History' }} />
      <Stack.Screen name="tokens" options={{ title: 'All Tokens' }} />
      <Stack.Screen name="import-token" options={{ title: 'Import crypto' }} />
    </Stack>
  );
};

export default WalletLayout;
