import React from 'react';
import { ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Icon, IconName } from './Icon';

export interface IconButtonProps {
  icon: IconName;
  onPress?: () => void;
  size?: number;
  variant?: 'plain' | 'surface';
  color?: string;
  disabled?: boolean;
  accessibilityLabel: string;
  style?: ViewStyle;
}

export const IconButton: React.FC<IconButtonProps> = ({
  icon,
  onPress,
  size = 22,
  variant = 'plain',
  color,
  disabled,
  accessibilityLabel,
  style,
}) => {
  const { colors, radius } = useTheme();
  const dim = 44; // min touch target
  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      accessibilityState={{ disabled: !!disabled }}
      style={[
        {
          width: dim,
          height: dim,
          alignItems: 'center',
          justifyContent: 'center',
          borderRadius: radius.pill,
          backgroundColor: variant === 'surface' ? colors.surfaceAlt : 'transparent',
          opacity: disabled ? 0.4 : 1,
        },
        style,
      ]}
    >
      <Icon name={icon} size={size} color={color ?? colors.textPrimary} />
    </PressableScale>
  );
};
