// components/kyc/StatusPanel.tsx
//
// The centred identity-verification status panel — hero icon ring, title,
// subtitle, actions — shared by the KYC and name-change screens.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Icon, Text } from '@/components/ui';

export function StatusPanel({
  icon,
  iconColor,
  iconBg,
  title,
  subtitle,
  children,
}: {
  icon: React.ComponentProps<typeof Icon>['name'];
  iconColor: string;
  iconBg: string;
  title: string;
  subtitle?: string;
  children?: React.ReactNode;
}) {
  const { spacing, radius } = useTheme();
  return (
    <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', paddingHorizontal: spacing.lg }}>
      <View
        style={{
          width: 96,
          height: 96,
          borderRadius: radius.pill,
          backgroundColor: iconBg,
          alignItems: 'center',
          justifyContent: 'center',
          marginBottom: spacing.xl,
        }}
      >
        <Icon name={icon} size={48} color={iconColor} />
      </View>
      <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.md }}>
        {title}
      </Text>
      {subtitle ? (
        <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing.xl }}>
          {subtitle}
        </Text>
      ) : null}
      {children}
    </View>
  );
}
