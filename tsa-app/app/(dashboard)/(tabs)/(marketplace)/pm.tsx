// app/(dashboard)/(tabs)/(marketplace)/pm.tsx
//
// PM (Product/Corporate Market) — dedicated screen. The corporate market-maker
// trading desk isn't live yet, so this is a themed coming-soon placeholder.
// Opened from the Marketplace "PM" chip (router.push('/pm')). Header is hidden
// in the marketplace _layout; the screen renders its own dark SafeAreaView.
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Badge, Button, Icon, IconButton, Text } from '@/components/ui';

export default function PMScreen() {
  const { colors, spacing, radius } = useTheme();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flexDirection: 'row', alignItems: 'center', padding: spacing.lg, gap: spacing.sm }}>
        <IconButton icon="chevronLeft" accessibilityLabel="Back" onPress={() => router.back()} />
        <Text variant="title3" weight="700">PM</Text>
        <Badge label="Corporate" size="sm" color={colors.warning} background={colors.warningBg} />
      </View>

      <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', padding: spacing.xl }}>
        <View
          style={{
            width: 72,
            height: 72,
            borderRadius: radius.lg,
            backgroundColor: colors.warningBg,
            alignItems: 'center',
            justifyContent: 'center',
            marginBottom: spacing.lg,
          }}
        >
          <Icon name="storefront" size={34} color={colors.warning} />
        </View>
        <Text variant="title2" weight="700" align="center">
          Corporate trading is coming soon
        </Text>
        <Text variant="body" color="textSecondary" align="center" style={{ marginTop: spacing.sm, maxWidth: 320 }}>
          PM lets you trade with corporate market makers for larger, faster fills.
          It isn’t available yet — for now you can trade peer-to-peer on P2P.
        </Text>
        <Button
          title="Go to P2P"
          variant="secondary"
          leftIcon="swap"
          fullWidth={false}
          onPress={() => router.replace('/p2p')}
          style={{ marginTop: spacing.xl }}
        />
      </View>
    </SafeAreaView>
  );
}
