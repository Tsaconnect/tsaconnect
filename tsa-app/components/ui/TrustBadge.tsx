// Seller trust tier chip (New / Verified / Trusted) + optional ⚡ auto-accept.
// Screen-level stat rows compose this with AmountText/Text; this is the tier atom.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Badge } from './Badge';

export type TrustTier = 'new' | 'verified' | 'trusted';

export interface TrustBadgeProps {
  tier: TrustTier;
  autoAccept?: boolean;
  size?: 'sm' | 'md';
}

export const TrustBadge: React.FC<TrustBadgeProps> = ({ tier, autoAccept, size = 'sm' }) => {
  const { colors, spacing } = useTheme();
  const tierProps =
    tier === 'trusted'
      ? { label: 'Trusted', icon: 'trusted' as const, color: colors.accent, background: colors.accentMuted }
      : tier === 'verified'
        ? { label: 'Verified', icon: 'verified' as const, color: colors.success, background: colors.successBg }
        : { label: 'New', color: colors.textSecondary, background: colors.surfaceAlt };

  return (
    <View style={{ flexDirection: 'row', gap: spacing.xs }}>
      <Badge size={size} {...tierProps} />
      {autoAccept && (
        <Badge size={size} label="Auto" icon="bolt" color={colors.warning} background={colors.warningBg} />
      )}
    </View>
  );
};
