// Stack layout for the legal documents (Privacy Policy, Terms of Service).
// Header chrome mirrors app/wallet/_layout.tsx so the section reads as part of
// the same app. Titles come from each screen's own <Stack.Screen> so the
// document title and the header can never drift apart.
import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

const LegalLayout = () => {
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

  return <Stack screenOptions={screenOptions} />;
};

export default LegalLayout;
