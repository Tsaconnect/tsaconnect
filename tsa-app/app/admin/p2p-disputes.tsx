// app/admin/p2p-disputes.tsx
//
// Admin dispute console — queue of open P2P disputes awaiting resolution.
// Gated to admin / super_admin (the backend layers RequireP2PAdmin on top of
// AdminAuth; we mirror that here so non-admins get a clear in-app message
// rather than a silent empty list from a 403).
//
// Each row renders the dispute context the list endpoint returns
// (GET /admin/p2p/disputes → []P2PDispute): the raising role, reason snippet,
// dispute status, evidence count, and age. Tapping a row opens the resolve
// detail screen keyed by tradeId (the resolve endpoints key off trade id).
//
// Note: the list endpoint returns bare dispute rows — it does not decorate
// them with trade amount/fiat/parties, and admins can't fetch the trade via
// the participant-gated /p2p/trades/:id. So the row shows dispute-level facts
// only; richer trade context would need a backend decoration (see report).
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  SegmentedControl,
  SkeletonGroup,
  StatusPill,
  Text,
} from '@/components/ui';
import { adminP2pApi, type P2PDispute } from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';

// Roles the backend's RequireP2PAdmin admits. 'superadmin' (no underscore) is
// tolerated because the marketplace flow historically stored it that way.
const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

type DisputeFilter = 'open' | 'resolved' | 'rejected';

const AdminDisputesScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { role, loading: roleLoading } = useUserRole();
  const [disputes, setDisputes] = useState<P2PDispute[]>([]);
  const [status, setStatus] = useState<DisputeFilter>('open');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);

  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const load = useCallback(async () => {
    if (!isAdmin) {
      setLoading(false);
      return;
    }
    setError(false);
    const res = await adminP2pApi.listDisputes(status);
    if (res.success && res.data) {
      setDisputes(res.data);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [isAdmin, status]);

  // Reload on focus so a dispute resolved on the detail screen drops out of the
  // open queue when the admin navigates back.
  useFocusEffect(
    useCallback(() => {
      if (!roleLoading) {
        setLoading(true);
        load();
      }
    }, [roleLoading, load]),
  );

  // ─── Role gate ──────────────────────────────────────────────────────────
  if (!roleLoading && !isAdmin) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="lock"
            title="Admins only"
            subtitle="Dispute resolution is restricted to admin and super-admin accounts."
            actionLabel="Go back"
            onAction={() => router.back()}
          />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            tintColor={colors.accent}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text variant="title1" weight="800">
          Disputes
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
          Review escrow disputes and resolve funds to the buyer or seller.
        </Text>

        <SegmentedControl
          segments={[
            { key: 'open', label: 'Open' },
            { key: 'resolved', label: 'Resolved' },
            { key: 'rejected', label: 'Rejected' },
          ]}
          value={status}
          onChange={(k) => {
            setStatus(k as DisputeFilter);
            setLoading(true);
          }}
        />

        <View style={{ marginTop: spacing.lg }}>
          {loading || roleLoading ? (
            <SkeletonGroup shape="tradeRow" count={5} />
          ) : error ? (
            <ErrorState
              subtitle="Couldn’t load the dispute queue. Check your connection."
              onRetry={() => {
                setLoading(true);
                load();
              }}
            />
          ) : disputes.length === 0 ? (
            <EmptyState
              icon="shield"
              title={status === 'open' ? 'No open disputes' : `No ${status} disputes`}
              subtitle={
                status === 'open'
                  ? 'Disputed trades land here for review. Pull to refresh.'
                  : 'Nothing in this bucket yet.'
              }
            />
          ) : (
            <View style={{ gap: spacing.md }}>
              {disputes.map((d) => (
                <DisputeRow key={d.id} dispute={d} />
              ))}
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const DisputeRow: React.FC<{ dispute: P2PDispute }> = ({ dispute }) => {
  const { colors, spacing } = useTheme();
  const railColor = dispute.status === 'open' ? colors.danger : colors.accentRail;
  const evidenceCount = dispute.evidenceUrls?.length ?? 0;

  return (
    <PressableScale onPress={() => router.push(`/admin/p2p-disputes/${dispute.tradeId}`)}>
      <AccentCard railColor={railColor}>
        <View style={{ flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between', gap: spacing.sm }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}>
            <Icon name="dispute" size={20} color={railColor} />
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                Trade #{shortId(dispute.tradeId)}
              </Text>
              <Text variant="caption" color="textMuted">
                Raised by {dispute.raisedRole} · {formatAge(dispute.createdAt)}
              </Text>
            </View>
          </View>
          <StatusPill kind="dispute" status={dispute.status} size="sm" />
        </View>

        <Divider spacing="sm" />

        <Text variant="overline" color="textMuted">
          REASON
        </Text>
        <Text variant="bodySm" numberOfLines={3} style={{ marginTop: 2 }}>
          {dispute.reason?.trim() || 'No reason provided.'}
        </Text>

        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginTop: spacing.md }}>
          <View style={{ flexDirection: 'row', gap: spacing.xs }}>
            <Badge
              label={dispute.raisedRole === 'buyer' ? 'Buyer raised' : 'Seller raised'}
              icon="user"
              size="sm"
            />
            {evidenceCount > 0 && (
              <Badge label={`${evidenceCount} photo${evidenceCount > 1 ? 's' : ''}`} icon="image" size="sm" />
            )}
          </View>
          <Icon name="chevronRight" size={18} color={colors.textMuted} />
        </View>
      </AccentCard>
    </PressableScale>
  );
};

// First segment of a UUID — enough to disambiguate in a queue without showing
// the full 36-char id.
function shortId(id: string): string {
  return id ? id.slice(0, 8) : '—';
}

function formatAge(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return iso;
  const mins = Math.round(ms / 60_000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.round(hrs / 24);
  return `${days}d ago`;
}

export default AdminDisputesScreen;
