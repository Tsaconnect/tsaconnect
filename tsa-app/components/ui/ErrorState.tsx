import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { Icon } from './Icon';
import { Button } from './Button';

export interface ErrorStateProps {
  title?: string;
  subtitle?: string;
  onRetry?: () => void;
  retryLabel?: string;
}

export const ErrorState: React.FC<ErrorStateProps> = ({
  title = 'Something went wrong',
  subtitle,
  onRetry,
  retryLabel = 'Try again',
}) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ alignItems: 'center', paddingVertical: spacing['4xl'], paddingHorizontal: spacing.xl, gap: spacing.sm }}>
      <View
        style={{
          width: 64,
          height: 64,
          borderRadius: 32,
          backgroundColor: colors.dangerBg,
          alignItems: 'center',
          justifyContent: 'center',
          marginBottom: spacing.sm,
        }}
      >
        <Icon name="warning" size={30} color={colors.danger} />
      </View>
      <Text variant="title3" weight="700" align="center">
        {title}
      </Text>
      {subtitle && (
        <Text variant="bodySm" color="textSecondary" align="center">
          {subtitle}
        </Text>
      )}
      {onRetry && (
        <Button title={retryLabel} onPress={onRetry} variant="secondary" leftIcon="refresh" fullWidth={false} size="sm" style={{ marginTop: spacing.sm, alignSelf: 'center' }} />
      )}
    </View>
  );
};
