import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { Icon, IconName } from './Icon';
import { Button } from './Button';

export interface EmptyStateProps {
  icon?: IconName;
  title: string;
  subtitle?: string;
  actionLabel?: string;
  onAction?: () => void;
}

export const EmptyState: React.FC<EmptyStateProps> = ({ icon = 'empty', title, subtitle, actionLabel, onAction }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ alignItems: 'center', paddingVertical: spacing['4xl'], paddingHorizontal: spacing.xl, gap: spacing.sm }}>
      <View
        style={{
          width: 64,
          height: 64,
          borderRadius: 32,
          backgroundColor: colors.surfaceAlt,
          alignItems: 'center',
          justifyContent: 'center',
          marginBottom: spacing.sm,
        }}
      >
        <Icon name={icon} size={30} color={colors.textMuted} />
      </View>
      <Text variant="title3" weight="700" align="center">
        {title}
      </Text>
      {subtitle && (
        <Text variant="bodySm" color="textSecondary" align="center">
          {subtitle}
        </Text>
      )}
      {actionLabel && onAction && (
        <Button title={actionLabel} onPress={onAction} variant="secondary" fullWidth={false} size="sm" style={{ marginTop: spacing.sm, alignSelf: 'center' }} />
      )}
    </View>
  );
};
