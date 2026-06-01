// components/p2p/SellerTrustBadges.tsx
//
// Renders a seller's trust signals from SellerStats. Compact row for ad cards;
// shows a "New" badge when the merchant has no completed trades yet.
//
// Migrated onto the TSA Signature design system (@/theme tokens + @/components/ui
// primitives). Light/dark both derive from useTheme — no hardcoded colors and
// no legacy constants/theme import. The stats→badges logic (New gate, online
// dot, trusted/verified cascade, orders/completion/release/positive stats) and
// the props/exports are preserved verbatim.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Badge, Icon, Text } from '@/components/ui';
import type { SellerStats } from '../../services/p2p';

function relTime(secs?: number): string | null {
  if (secs == null) return null;
  if (secs < 90) return `~${secs}s`;
  return `~${Math.round(secs / 60)}m`;
}

const SellerTrustBadges: React.FC<{ stats?: SellerStats }> = ({ stats }) => {
  const { colors, spacing } = useTheme();

  if (!stats || stats.isNew) {
    return (
      <View style={{ flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: spacing.xs, marginTop: spacing.xs }}>
        <Badge label="New" size="sm" color={colors.info} background={colors.infoBg} />
      </View>
    );
  }

  const release = relTime(stats.avgReleaseSecs);
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: spacing.xs, marginTop: spacing.xs }}>
      {stats.online && (
        <View
          style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: colors.online }}
        />
      )}
      {stats.trusted ? (
        <Badge label="Trusted" icon="verified" size="sm" color={colors.accent} background={colors.accentMuted} />
      ) : stats.verified ? (
        <Badge label="Verified" icon="checkCircle" size="sm" color={colors.success} background={colors.successBg} />
      ) : null}
      <Text variant="overline" color="textMuted" weight="600">
        {stats.totalOrders} orders
      </Text>
      {stats.completionRate != null && (
        <Text variant="overline" color="textMuted" weight="600">
          · {(stats.completionRate * 100).toFixed(0)}%
        </Text>
      )}
      {release && (
        <Text variant="overline" color="textMuted" weight="600">
          · releases {release}
        </Text>
      )}
      {stats.positivePct != null && (
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Text variant="overline" color="textMuted" weight="600">
            ·{' '}
          </Text>
          <Icon name="thumbUp" size={11} color={colors.textMuted} />
          <Text variant="overline" color="textMuted" weight="600">
            {' '}
            {(stats.positivePct * 100).toFixed(0)}%
          </Text>
        </View>
      )}
    </View>
  );
};

export default SellerTrustBadges;
