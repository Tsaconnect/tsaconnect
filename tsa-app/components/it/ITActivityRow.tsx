// One row of Instant Transfer activity, shared by the hub and the full list.
//
//   [↓]  Buy USDC                    ₦800
//        2h ago            [Awaiting transfer]
//
// Every text node is single-line and the right column never shrinks below its
// content, so a long status can no longer push past the card edge — the bug
// the old SummaryRow layout had with "Expired — no payment received".
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Badge, Icon, PressableScale, Text, useToneColors } from '@/components/ui';
import { relativeTime, type ITActivity } from '@/utils/itActivity';

const ICON_SIZE = 36;

/** Divider inset that lines up with the row's text, past the direction icon. */
export const ACTIVITY_DIVIDER_INSET = ICON_SIZE + 12; // spacing.md

export default function ITActivityRow({ item }: { item: ITActivity }) {
  const { colors, spacing, radius } = useTheme();
  const status = useToneColors(item.status.tone);
  const isBuy = item.direction === 'buy';
  const title = `${isBuy ? 'Buy' : 'Sell'} ${item.asset}`;
  const when = relativeTime(item.at);

  return (
    <PressableScale
      onPress={() => router.push(item.href as never)}
      accessibilityRole="button"
      accessibilityLabel={`${title}, ${item.amount}, ${item.status.label}${when ? `, ${when}` : ''}`}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingVertical: spacing.md,
      }}
    >
      <View
        style={{
          width: ICON_SIZE,
          height: ICON_SIZE,
          borderRadius: radius.pill,
          alignItems: 'center',
          justifyContent: 'center',
          backgroundColor: isBuy ? colors.successBg : colors.accentMuted,
        }}
      >
        <Icon name={isBuy ? 'arrowDown' : 'arrowUp'} size={16} color={isBuy ? colors.success : colors.accent} />
      </View>

      <View style={{ flex: 1, minWidth: 0, gap: 2 }}>
        <Text variant="bodySm" weight="600" numberOfLines={1} style={{ color: colors.textPrimary }}>
          {title}
        </Text>
        <Text variant="caption" numberOfLines={1} style={{ color: colors.textMuted }}>
          {when}
        </Text>
      </View>

      <View style={{ alignItems: 'flex-end', gap: 4, flexShrink: 0, maxWidth: '55%' }}>
        <Text variant="bodySm" weight="700" numeric numberOfLines={1} style={{ color: colors.textPrimary }}>
          {item.amount}
        </Text>
        <Badge
          label={item.status.label}
          color={status.color}
          background={status.bg}
          size="sm"
          style={{ alignSelf: 'flex-end' }}
        />
      </View>
    </PressableScale>
  );
}
