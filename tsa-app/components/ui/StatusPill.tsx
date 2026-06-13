// Namespace-aware status pill — one consistent rendering of every trade/ad/dispute
// status across the app. Resolves the status `tone` to theme color/bg.
import React from 'react';
import { ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Badge } from './Badge';
import { StatusKind, StatusTone, getStatusVisual } from './statusMap';

export interface StatusPillProps {
  kind: StatusKind;
  status: string;
  size?: 'sm' | 'md';
  style?: ViewStyle;
}

export function useToneColors(tone: StatusTone): { color: string; bg: string } {
  const { colors } = useTheme();
  switch (tone) {
    case 'success':
      return { color: colors.success, bg: colors.successBg };
    case 'warning':
      return { color: colors.warning, bg: colors.warningBg };
    case 'danger':
      return { color: colors.danger, bg: colors.dangerBg };
    case 'info':
      return { color: colors.info, bg: colors.infoBg };
    case 'escrow':
      return { color: colors.escrow, bg: colors.escrowBg };
    case 'accent':
      return { color: colors.accent, bg: colors.accentMuted };
    case 'neutral':
    default:
      return { color: colors.textSecondary, bg: colors.surfaceAlt };
  }
}

export const StatusPill: React.FC<StatusPillProps> = ({ kind, status, size = 'md', style }) => {
  const visual = getStatusVisual(kind, status);
  const { color, bg } = useToneColors(visual.tone);
  return <Badge label={visual.label} color={color} background={bg} size={size} style={style} />;
};
