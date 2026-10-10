// components/pay/PayLinkEntry.tsx
//
// Where a TSA Pay merchant starts a payment link: a Request payment action and
// a Payment links row (and her Refunds). The same pair sits on Payment Settings and on the
// merchant dashboard's TSA Pay card; the card also carries the Payment settings
// row, since it is an approved merchant's one way back to where she is paid.
import React from 'react';
import { View } from 'react-native';
import { router, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Icon, PressableScale, Text, type IconName } from '@/components/ui';
import { PAY_LINK_NEW, PAY_LINKS, PAY_REFUNDS, PAY_SETTINGS } from '@/constants/routes';

/**
 * `canRequest` is false for a suspended merchant: she may still read her links.
 * `withSettings` adds the Payment settings row (off on Payment Settings itself).
 */
export function PayLinkEntry({ canRequest = true, withSettings = false }: { canRequest?: boolean; withSettings?: boolean }) {
  const { spacing } = useTheme();
  return (
    <View style={{ gap: spacing.sm }}>
      {canRequest ? (
        <Button title="Request payment" leftIcon="send" onPress={() => router.push(PAY_LINK_NEW)} />
      ) : null}
      <EntryRow icon="list" label="Payment links" href={PAY_LINKS} />
      <EntryRow icon="refresh" label="Refunds" href={PAY_REFUNDS} />
      {withSettings ? <EntryRow icon="bank" label="Payment settings" href={PAY_SETTINGS} /> : null}
    </View>
  );
}

function EntryRow({ icon, label, href }: { icon: IconName; label: string; href: Href }) {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={() => router.push(href)}
      accessibilityRole="button"
      accessibilityLabel={label}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        padding: spacing.md,
        borderRadius: radius.md,
        backgroundColor: colors.surfaceAlt,
      }}
    >
      <Icon name={icon} size={20} color={colors.accent} />
      <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
        {label}
      </Text>
      <Icon name="chevronRight" size={20} color={colors.textMuted} />
    </PressableScale>
  );
}
