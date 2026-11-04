// components/wallet/QuickActions.tsx
//
// The wallet balance card's row of actions. The tiles share the row equally
// (flex: 1, no minimum width) and a label may wrap to two lines, so the row fits
// a 320 pt screen: 320 − 2×16 (screen gutter) − 2×20 (card padding) = 248 pt,
// 62 pt a tile with four, room for the 44 pt icon circle. Swap has no tile here:
// it opens the same screen as the Easyswap tab.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Icon, PressableScale, Text, type IconName } from '@/components/ui';

export const WALLET_QUICK_ACTIONS = [
  { key: 'fund', label: 'Deposit', icon: 'bank' },
  { key: 'send', label: 'Withdraw', icon: 'send' },
  { key: 'instant-pay', label: 'Instant Pay', icon: 'bolt' },
  { key: 'pay-code', label: 'TSA Pay', icon: 'storefront' },
] as const satisfies readonly { key: string; label: string; icon: IconName }[];

export type WalletQuickActionKey = (typeof WALLET_QUICK_ACTIONS)[number]['key'];

export function QuickActions({ onPress }: { onPress: (key: WalletQuickActionKey) => void }) {
  const { colors, spacing, radius } = useTheme();
  return (
    <View style={{ flexDirection: 'row' }}>
      {WALLET_QUICK_ACTIONS.map((action) => (
        <PressableScale key={action.key} style={{ flex: 1, alignItems: 'center' }} onPress={() => onPress(action.key)}>
          <View
            style={{
              width: 44,
              height: 44,
              borderRadius: radius.pill,
              backgroundColor: colors.accentMuted,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.xs,
            }}
          >
            <Icon name={action.icon} size={20} color={colors.accent} />
          </View>
          <Text variant="caption" color="textSecondary" align="center" numberOfLines={2}>
            {action.label}
          </Text>
        </PressableScale>
      ))}
    </View>
  );
}
