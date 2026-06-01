// components/common/ErrorState.tsx
//
// Inline error-state component. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives) — visual/structural restyle only.
// Props interface ({ message, onRetry }) and the named `ErrorState` export are
// preserved verbatim so existing consumers keep working; a separate design-system
// ErrorState lives at @/components/ui (different `subtitle`-based API).
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text, Button, Icon } from '@/components/ui';

interface ErrorStateProps {
  message: string;
  onRetry?: () => void;
}

export const ErrorState = ({ message, onRetry }: ErrorStateProps) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        paddingHorizontal: spacing['3xl'],
        paddingVertical: spacing['4xl'],
        gap: spacing.sm,
      }}
    >
      <View
        style={{
          width: 96,
          height: 96,
          borderRadius: radius.pill,
          backgroundColor: colors.dangerBg,
          justifyContent: 'center',
          alignItems: 'center',
          marginBottom: spacing.lg,
        }}
      >
        <Icon name="warning" size={48} color={colors.danger} />
      </View>
      <Text variant="title2" weight="700" align="center">
        Something went wrong
      </Text>
      <Text variant="body" color="textSecondary" align="center">
        {message}
      </Text>
      {onRetry && (
        <Button
          title="Try Again"
          onPress={onRetry}
          leftIcon="refresh"
          fullWidth={false}
          style={{ marginTop: spacing.xl }}
        />
      )}
    </View>
  );
};
