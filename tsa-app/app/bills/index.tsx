// app/bills/index.tsx
//
// Pay-bills hub — one tile per bill category. Each routes to the shared
// purchase screen (/bills/<category>). Bills are paid with spendable MCGP.
import React from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Card, Icon, IconName, PressableScale, Text } from '@/components/ui';
import type { BillCategory } from '@/services/billsApi';

type Tone = 'accent' | 'success' | 'info' | 'escrow' | 'warning';

interface CategoryTile {
  category: BillCategory;
  title: string;
  description: string;
  icon: IconName;
  tone: Tone;
}

const TILES: CategoryTile[] = [
  { category: 'airtime', title: 'Airtime', description: 'Top up any Nigerian number', icon: 'mobile', tone: 'accent' },
  { category: 'data', title: 'Data', description: 'Buy a data bundle', icon: 'wifi', tone: 'info' },
  { category: 'electricity', title: 'Electricity', description: 'Pay a prepaid/postpaid meter', icon: 'bolt', tone: 'warning' },
  { category: 'cabletv', title: 'Cable TV', description: 'DStv, GOtv, Startimes', icon: 'play', tone: 'success' },
  { category: 'betting', title: 'Betting', description: 'Fund a betting account', icon: 'gift', tone: 'escrow' },
  { category: 'epin', title: 'Exam PINs', description: 'WAEC & JAMB result/registration PINs', icon: 'card', tone: 'info' },
];

export default function BillsHub() {
  const { colors, spacing } = useTheme();

  const toneColors = (tone: Tone): { fg: string; bg: string } => {
    switch (tone) {
      case 'success':
        return { fg: colors.success, bg: colors.successBg };
      case 'info':
        return { fg: colors.info, bg: colors.infoBg };
      case 'escrow':
        return { fg: colors.escrow, bg: colors.escrowBg };
      case 'warning':
        return { fg: colors.warning, bg: colors.warningBg };
      case 'accent':
      default:
        return { fg: colors.accent, bg: colors.accentMuted };
    }
  };

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <Text variant="bodySm" style={{ color: colors.textSecondary, flex: 1 }}>
            Pay with your MCGP balance — no gas, nothing to sign.
          </Text>
          <PressableScale
            onPress={() => router.push('/bills/orders' as any)}
            accessibilityRole="button"
            accessibilityLabel="Bill history"
            style={{ flexDirection: 'row', alignItems: 'center', gap: 4, paddingVertical: 6, paddingLeft: 12 }}
          >
            <Icon name="clock" size={16} color={colors.accent} />
            <Text variant="bodySm" weight="600" style={{ color: colors.accent }}>
              History
            </Text>
          </PressableScale>
        </View>
        {TILES.map((t) => {
          const tc = toneColors(t.tone);
          return (
            <PressableScale key={t.category} onPress={() => router.push(`/bills/${t.category}` as any)}>
              <Card>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <View
                    style={{
                      width: 44,
                      height: 44,
                      borderRadius: 12,
                      backgroundColor: tc.bg,
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                  >
                    <Icon name={t.icon} size={22} color={tc.fg} />
                  </View>
                  <View style={{ flex: 1 }}>
                    <Text variant="body" weight="700" style={{ color: colors.textPrimary }}>
                      {t.title}
                    </Text>
                    <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                      {t.description}
                    </Text>
                  </View>
                  <Icon name="chevronRight" size={18} color={colors.textMuted} />
                </View>
              </Card>
            </PressableScale>
          );
        })}
      </ScrollView>
    </SafeAreaView>
  );
}
