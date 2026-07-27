// Catch-all for unmatched routes. Without this file expo-router falls back to
// its built-in "Unmatched Route" developer screen, which ships in release
// builds — a bad internal link or a stale deep link would show the user raw
// router diagnostics. This gives them a way back instead.
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router, usePathname } from 'expo-router';
import { useTheme } from '@/theme';
import { EmptyState, Text } from '@/components/ui';

export default function NotFoundScreen() {
  const { colors, spacing } = useTheme();
  const pathname = usePathname();

  const goHome = () => {
    if (router.canGoBack()) {
      router.back();
      return;
    }
    router.replace('/home');
  };

  return (
    <>
      <Stack.Screen options={{ title: 'Page not found' }} />
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <EmptyState
            icon="search"
            title="We couldn't find that page"
            subtitle="The link may be out of date, or the screen may have moved."
            actionLabel="Go Back"
            onAction={goHome}
          />
          {/* The path is useful in a support ticket but must not dominate the
              screen — muted caption, not part of the message. */}
          {!!pathname && (
            <Text
              variant="caption"
              color="textMuted"
              style={{ marginTop: spacing.lg, paddingHorizontal: spacing.xl }}
            >
              {pathname}
            </Text>
          )}
        </View>
      </SafeAreaView>
    </>
  );
}
