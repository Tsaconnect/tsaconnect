// Primary action primitive. Variants × sizes, loading state (stable width),
// optional icons, spring press. AA-correct foreground per variant.
import React from 'react';
import { ActivityIndicator, View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success';
export type ButtonSize = 'sm' | 'md' | 'lg';

export interface ButtonProps {
  title: string;
  /** Optional second line under the title (e.g. a price), for labels too long for one. */
  subtitle?: string;
  onPress?: () => void;
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  disabled?: boolean;
  leftIcon?: IconName;
  rightIcon?: IconName;
  fullWidth?: boolean;
  style?: ViewStyle;
  accessibilityLabel?: string;
}

const HEIGHT: Record<ButtonSize, number> = { sm: 38, md: 48, lg: 56 };

export const Button: React.FC<ButtonProps> = ({
  title,
  subtitle,
  onPress,
  variant = 'primary',
  size = 'md',
  loading,
  disabled,
  leftIcon,
  rightIcon,
  fullWidth = true,
  style,
  accessibilityLabel,
}) => {
  const { colors, radius, spacing } = useTheme();
  const isDisabled = disabled || loading;

  const palette: Record<ButtonVariant, { bg: string; fg: string; border?: string }> = {
    primary: { bg: colors.accent, fg: colors.onAccent },
    secondary: { bg: colors.surfaceAlt, fg: colors.textPrimary, border: colors.border },
    ghost: { bg: 'transparent', fg: colors.accent },
    danger: { bg: colors.danger, fg: '#FFFFFF' },
    success: { bg: colors.success, fg: '#FFFFFF' },
  };
  const p = palette[variant];
  const fg = p.fg;
  const fontVariant = size === 'sm' ? 'bodySm' : 'bodyMed';

  return (
    <PressableScale
      onPress={onPress}
      disabled={isDisabled}
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel ?? (subtitle ? `${title}, ${subtitle}` : title)}
      accessibilityState={{ disabled: !!isDisabled, busy: !!loading }}
      style={[
        {
          height: HEIGHT[size],
          flexDirection: 'row',
          alignItems: 'center',
          justifyContent: 'center',
          gap: spacing.sm,
          paddingHorizontal: size === 'sm' ? spacing.md : spacing.lg,
          borderRadius: radius.md,
          backgroundColor: p.bg,
          borderWidth: p.border ? 1 : 0,
          borderColor: p.border,
          opacity: isDisabled ? 0.5 : 1,
          alignSelf: fullWidth ? 'stretch' : 'flex-start',
        },
        style,
      ]}
    >
      {loading ? (
        <ActivityIndicator color={fg} />
      ) : (
        <>
          {leftIcon && <Icon name={leftIcon} size={size === 'sm' ? 16 : 18} color={fg} />}
          {subtitle ? (
            <View style={{ alignItems: 'center', flexShrink: 1 }}>
              <Text variant="bodySm" weight="700" numberOfLines={1} style={{ color: fg }}>
                {title}
              </Text>
              <Text variant="caption" numeric numberOfLines={1} adjustsFontSizeToFit style={{ color: fg, opacity: 0.85 }}>
                {subtitle}
              </Text>
            </View>
          ) : (
            <Text variant={fontVariant} weight="600" style={{ color: fg }}>
              {title}
            </Text>
          )}
          {rightIcon && <Icon name={rightIcon} size={size === 'sm' ? 16 : 18} color={fg} />}
        </>
      )}
    </PressableScale>
  );
};
