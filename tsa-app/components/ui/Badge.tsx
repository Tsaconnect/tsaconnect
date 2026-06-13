// Base label chip. StatusPill/TrustBadge are presets over this one primitive.
import React from 'react';
import { View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export interface BadgeProps {
  label: string;
  color?: string; // text/icon color (default textSecondary)
  background?: string; // pill fill (default surfaceAlt)
  icon?: IconName;
  size?: 'sm' | 'md';
  style?: ViewStyle;
}

export const Badge: React.FC<BadgeProps> = ({ label, color, background, icon, size = 'md', style }) => {
  const { colors, radius, spacing } = useTheme();
  const fg = color ?? colors.textSecondary;
  const bg = background ?? colors.surfaceAlt;
  const padV = size === 'sm' ? 2 : 4;
  const padH = size === 'sm' ? spacing.sm : spacing.md;
  const iconSize = size === 'sm' ? 12 : 14;
  return (
    <View
      style={[
        {
          flexDirection: 'row',
          alignItems: 'center',
          alignSelf: 'flex-start',
          gap: 4,
          paddingVertical: padV,
          paddingHorizontal: padH,
          borderRadius: radius.pill,
          backgroundColor: bg,
        },
        style,
      ]}
    >
      {icon && <Icon name={icon} size={iconSize} color={fg} />}
      <Text variant={size === 'sm' ? 'overline' : 'caption'} weight="600" style={{ color: fg }}>
        {label}
      </Text>
    </View>
  );
};
