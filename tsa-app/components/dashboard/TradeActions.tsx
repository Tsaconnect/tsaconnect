// components/dashboard/TradeActions.tsx
//
// "Trade Now" shortcut cards (Buy Product / Order Services / Trade & Earn /
// Trade Crypto). Trade & Earn opens the Trade Points screen; Trade Crypto opens
// the OTC / Spot Trade / All Swap page.
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Card, Icon, PressableScale, Text } from '@/components/ui';
import type { IconName } from '@/components/ui';

const TRADE_ITEMS: {
  key: string;
  title: string;
  subtitle: string;
  icon: IconName;
  tone: 'accent' | 'info' | 'success';
  route: string;
}[] = [
  {
    key: 'product',
    title: 'Buy Product',
    subtitle: 'Browse marketplace',
    icon: 'storefront',
    tone: 'accent',
    route: '/(dashboard)/(tabs)/(marketplace)',
  },
  {
    key: 'services',
    title: 'Order Services',
    subtitle: 'Find service providers',
    icon: 'list',
    tone: 'info',
    route: '/(dashboard)/(tabs)/(services)/serviceshome',
  },
  {
    key: 'trade',
    title: 'Trade & Earn',
    subtitle: 'Trade Points, cashback and rewards',
    icon: 'trendingUp',
    tone: 'success',
    route: '/trade',
  },
  {
    key: 'trade-crypto',
    title: 'Trade Crypto',
    subtitle: 'OTC, Spot Trade and All Swap',
    icon: 'swap',
    tone: 'accent',
    route: '/easyswap',
  },
];

export const TradeActions: React.FC = () => {
  const { colors, spacing } = useTheme();

  const toneFg: Record<'accent' | 'info' | 'success', string> = {
    accent: colors.accent,
    info: colors.info,
    success: colors.success,
  };
  const toneBg: Record<'accent' | 'info' | 'success', string> = {
    accent: colors.accentMuted,
    info: colors.infoBg,
    success: colors.successBg,
  };

  return (
    <View style={{ marginTop: spacing.xl }}>
      <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
        Trade Now
      </Text>
      {TRADE_ITEMS.map((item) => (
        <PressableScale
          key={item.key}
          onPress={() => router.push(item.route as any)}
          style={{ marginBottom: spacing.sm }}
        >
          <Card padding="md">
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <View
                style={{
                  width: 44,
                  height: 44,
                  borderRadius: 12,
                  backgroundColor: toneBg[item.tone],
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
              >
                <Icon name={item.icon} size={24} color={toneFg[item.tone]} />
              </View>
              <View style={{ flex: 1 }}>
                <Text variant="bodySm" weight="600">
                  {item.title}
                </Text>
                <Text variant="caption" color="textMuted">
                  {item.subtitle}
                </Text>
              </View>
              <Icon name="chevronRight" size={22} color={colors.textMuted} />
            </View>
          </Card>
        </PressableScale>
      ))}
    </View>
  );
};

export default TradeActions;
