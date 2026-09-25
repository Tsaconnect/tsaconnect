// Themed container — background + radius + optional elevation. Base for Card.
import React from 'react';
import { StyleSheet, View, ViewProps, ViewStyle } from 'react-native';
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

/** True for a colour with alpha below 1: #RGBA, #RRGGBBAA, rgba()/hsla(), or 'transparent'. */
export function isTranslucentColor(color: unknown): boolean {
  if (typeof color !== 'string') return false;
  const c = color.trim().toLowerCase();
  if (c === 'transparent') return true;
  const hex = /^#([0-9a-f]{4}|[0-9a-f]{8})$/.exec(c);
  if (hex) return !hex[1].endsWith(hex[1].length === 4 ? 'f' : 'ff');
  const fn = /^(?:rgba|hsla)\(.*,\s*([\d.]+%?)\s*\)$/.exec(c);
  if (fn) return fn[1].endsWith('%') ? parseFloat(fn[1]) < 100 : parseFloat(fn[1]) < 1;
  return false;
}

export const Surface: React.FC<SurfaceProps> = ({
  radius = 'lg',
  padding, // no default: undefined => no padding (Card supplies its own default)
  elevation = 'none',
  background = 'surface',
  bordered,
  style,
  children,
  ...rest
}) => {
  const { colors, radius: r, spacing } = useTheme();
  // A shadow under a see-through fill shows through it on Android — the muddy
  // inner layer on every tinted status card. A tinted surface stays flat.
  const tinted = isTranslucentColor(StyleSheet.flatten(style)?.backgroundColor);
  const elevationStyle = useElevationStyle(tinted ? 'none' : elevation);
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
