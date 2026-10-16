// components/dashboard/BalanceCard.tsx
//
// Total-portfolio hero with the display-currency picker, activity history, the
// value-hide toggle and the four quick actions (Buy / Services / Trade / Pay).
// Pay opens TSA Pay by code; the wallet already has its own tab, so it gave up its tile here.
// The currency and history controls live here, not in a page header, because
// they are about this balance. Migrated onto the TSA Signature design
// system — the dual-price reads, the visibility toggle, and every quick-action
// route are preserved verbatim; only presentation moved to @/theme tokens.
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useTheme } from '@/theme';
import { Card, Divider, Icon, IconButton, PressableScale, Text } from '@/components/ui';
import CurrencySelector from '@/components/currency/CurrencySelector';
import { PAY_CODE } from '@/constants/routes';
import type { IconName } from '@/components/ui';

const EYE: IconName = 'eye';
const EYE_OFF: IconName = 'eyeOff';

interface BalanceCardProps {
  totalUsdValue: number;
  dailyChange: number;
  isValuesHidden: boolean;
  onToggleVisibility: () => void;
}

const QUICK_ACTIONS: { key: string; label: string; icon: IconName; route: string }[] = [
  { key: 'buy', label: 'Buy', icon: 'storefront', route: '/(dashboard)/(tabs)/(marketplace)' },
  { key: 'services', label: 'Services', icon: 'list', route: '/(dashboard)/(tabs)/(services)/serviceshome' },
  { key: 'trade', label: 'Trade', icon: 'trendingUp', route: '/easyswap' },
  { key: 'pay', label: 'TSA Pay', icon: 'card', route: PAY_CODE },
];

export const BalanceCard: React.FC<BalanceCardProps> = ({
  totalUsdValue,
  dailyChange,
  isValuesHidden,
  onToggleVisibility,
}) => {
  const { colors, spacing } = useTheme();
  const { formatDualPrice } = useCurrency();
  const dualBalance = formatDualPrice(totalUsdValue);
  const dualChange = formatDualPrice(Math.abs(dailyChange));
  const isUp = dailyChange >= 0;
  const changeColor = isUp ? colors.success : colors.danger;
  const changePrefix = isUp ? '+' : '-';

  return (
    <Card elevation="md" padding="xl" style={{ marginTop: spacing.sm }}>
      <View
        style={{
          flexDirection: 'row',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: spacing.sm,
        }}
      >
        <Text variant="caption" color="textSecondary">
          Total Portfolio
        </Text>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <CurrencySelector variant="header" />
          <IconButton
            icon="clock"
            size={20}
            color={colors.accent}
            onPress={() => router.push('/activity' as never)}
            accessibilityLabel="Activity"
          />
          <IconButton
            icon={isValuesHidden ? EYE_OFF : EYE}
            size={20}
            color={colors.textMuted}
            onPress={onToggleVisibility}
            accessibilityLabel={isValuesHidden ? 'Show balances' : 'Hide balances'}
          />
        </View>
      </View>

      <Text variant="display" weight="700" numeric>
        {isValuesHidden ? '••••••' : dualBalance.primary}
      </Text>
      <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
        {isValuesHidden ? '••••' : dualBalance.secondary}
      </Text>

      {dailyChange !== 0 && (
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            gap: spacing.xs,
            marginTop: spacing.xs,
          }}
        >
          <Icon name={isUp ? 'trendingUp' : 'trendingDown'} size={14} color={changeColor} />
          <Text variant="caption" weight="600" style={{ color: changeColor }}>
            {changePrefix}{dualChange.primary} today
          </Text>
        </View>
      )}

      <Divider spacing="lg" />

      <View style={{ flexDirection: 'row', justifyContent: 'space-around' }}>
        {QUICK_ACTIONS.map((action) => (
          <PressableScale
            key={action.key}
            onPress={() => router.push(action.route as any)}
            style={{ alignItems: 'center', minWidth: 56, gap: spacing.xs }}
          >
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: 22,
                backgroundColor: colors.accentMuted,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name={action.icon} size={20} color={colors.accent} />
            </View>
            <Text variant="caption" color="textSecondary">
              {action.label}
            </Text>
          </PressableScale>
        ))}
      </View>
    </Card>
  );
};

export default BalanceCard;
