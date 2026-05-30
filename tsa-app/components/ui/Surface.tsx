// Themed container — background + radius + optional elevation. Base for Card.
import React from 'react';
import { View, ViewProps, ViewStyle } from 'react-native';
import { RadiusKey, SpacingKey, useTheme } from '@/theme';

export type Elevation = 'none' | 'sm' | 'md' | 'lg';

export interface SurfaceProps extends ViewProps {
  radius?: RadiusKey;
  padding?: SpacingKey;
  elevation?: Elevation;
  /** Token key for the surface background (default 'surface'). */
  background?: 'surface' | 'surfaceAlt' | 'surfaceElevated' | 'background';
  bordered?: boolean;
}

export function useElevationStyle(elevation: Elevation): ViewStyle {
  const { isDark } = useTheme();
  if (elevation === 'none' || isDark) {
    // Shadows read poorly on dark surfaces; rely on surfaceElevated contrast instead.
    return {};
  }
  const map: Record<Exclude<Elevation, 'none'>, ViewStyle> = {
    sm: { shadowColor: '#16201F', shadowOpacity: 0.06, shadowRadius: 4, shadowOffset: { width: 0, height: 1 }, elevation: 1 },
    md: { shadowColor: '#16201F', shadowOpacity: 0.08, shadowRadius: 10, shadowOffset: { width: 0, height: 3 }, elevation: 3 },
    lg: { shadowColor: '#16201F', shadowOpacity: 0.1, shadowRadius: 18, shadowOffset: { width: 0, height: 6 }, elevation: 6 },
  };
  return map[elevation];
}

export const Surface: React.FC<SurfaceProps> = ({
  radius = 'lg',
  padding,
  elevation = 'none',
  background = 'surface',
  bordered,
  style,
  children,
  ...rest
}) => {
  const { colors, radius: r, spacing } = useTheme();
  const elevationStyle = useElevationStyle(elevation);
  return (
    <View
      style={[
        {
          backgroundColor: colors[background],
          borderRadius: r[radius],
          padding: padding ? spacing[padding] : undefined,
          borderWidth: bordered ? 1 : 0,
          borderColor: bordered ? colors.border : undefined,
        },
        elevationStyle,
        style,
      ]}
      {...rest}
    >
      {children}
    </View>
  );
};
