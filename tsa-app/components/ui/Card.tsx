// Card = elevated Surface preset. AccentCard adds the signature start-edge rail
// (logical edge → flips under RTL). Rail color is overridable (e.g. a status hue).
import React from 'react';
import { View } from 'react-native';
import { SpacingKey, useTheme } from '@/theme';
import { Surface, SurfaceProps } from './Surface';

export type CardProps = SurfaceProps;

export const Card: React.FC<CardProps> = ({ elevation = 'sm', padding = 'lg', children, ...rest }) => (
  <Surface elevation={elevation} padding={padding} {...rest}>
    {children}
  </Surface>
);

export interface AccentCardProps extends Omit<CardProps, 'padding'> {
  /** Rail color (default theme accentRail). Pass a status hue to color-code the card. */
  railColor?: string;
  padding?: SpacingKey;
  railWidth?: number;
}

export const AccentCard: React.FC<AccentCardProps> = ({
  railColor,
  railWidth = 4,
  padding = 'lg',
  elevation = 'sm',
  style,
  children,
  ...rest
}) => {
  const { colors, spacing } = useTheme();
  // Render Surface directly with NO padding so the rail can sit flush at the
  // start edge; the inner View owns the content padding (+ rail offset).
  return (
    <Surface elevation={elevation} style={[{ overflow: 'hidden' }, style]} {...rest}>
      <View
        style={{
          position: 'absolute',
          top: 0,
          bottom: 0,
          start: 0,
          width: railWidth,
          backgroundColor: railColor ?? colors.accentRail,
        }}
      />
      <View style={{ padding: spacing[padding], paddingStart: spacing[padding] + railWidth }}>{children}</View>
    </Surface>
  );
};
