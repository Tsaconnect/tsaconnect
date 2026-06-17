// app/admin/pm-status.tsx
//
// Minimal PM status view — shows the active Payment Merchant's status,
// virtual account (VA / inbound account number), available + reserved NGN
// balances, and trade caps.
//
// *** Backend gap note ***
// There is no dedicated GET /admin/pm or GET /admin/pm/:id list endpoint on
// the backend. The PMHandler only exposes:
//   GET  /admin/pm/manual-queue   (returns trade queue items, not PM rows)
//   POST /admin/pm/:id/withdraw
// Consequently, this screen derives what it can from the approved MerchantRequest
// rows with businessType === 'p2p_merchant' (which carry the requested caps) and
// surfaces a message prompting the operator to check the DB or the backend team
// to add a PM-list endpoint. See task-8-report.md §Endpoint Gaps.
//
// When a PM-list GET endpoint is added to the backend, replace the
// listApplications call below with the dedicated endpoint and update the types
// to include inboundAccountNumber / nombaSubAccountId from PaymentMerchant.
//
// Pattern mirrors app/admin/pm-queue.tsx and app/admin/p2p-disputes.tsx.
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Divider,
  EmptyState,
  ErrorState,
  SkeletonGroup,
  SummaryRow,
  Text,
} from '@/components/ui';
import { pmAdminApi, type MerchantRequest } from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

// We fetch all merchant-requests with status=approved|pending (both) to surface
// activated PMs. The list endpoint only supports status= single value, so we
// issue one call for approved rows with businessType === 'p2p_merchant'.
const PMStatusScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { role, loading: roleLoading } = useUserRole();

  const [approvedPMs, setApprovedPMs] = useState<MerchantRequest[]>([]);
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
    // Fetch approved merchant requests; filter client-side to p2p_merchant.
    // Note: The backend ListMerchantRequests supports only a single status=
    // query param; "approved" covers provisioned PMs.
    const res = await pmAdminApi.listApprovedApplications();
    if (res.success && res.data) {
      setApprovedPMs(res.data.requests);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [isAdmin]);

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
            subtitle="PM status is restricted to admin and super-admin accounts."
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
        <Text variant="title1" weight="700">
          PM Status
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.xs }}>
          Approved Payment Merchants. NGN balance data requires a backend PM-list endpoint (see gap note).
        </Text>
        <Badge
          label="Read-only · NGN balance not yet available from backend"
          icon="info"
          size="sm"
        />

        <View style={{ marginTop: spacing.lg }}>
          {loading || roleLoading ? (
            <SkeletonGroup shape="tradeRow" count={3} />
          ) : error ? (
            <ErrorState
              subtitle="Couldn't load PM status. Check your connection."
              onRetry={() => {
                setLoading(true);
                load();
              }}
            />
          ) : approvedPMs.length === 0 ? (
            <EmptyState
              icon="checkCircle"
              title="No approved PMs yet"
              subtitle="Approved p2p_merchant applications will appear here after provisioning."
            />
          ) : (
            <View style={{ gap: spacing.md }}>
              {approvedPMs.map((pm) => (
                <PMStatusCard key={pm.id} pm={pm} />
              ))}
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const PMStatusCard: React.FC<{ pm: MerchantRequest }> = ({ pm }) => {
  const { colors, spacing } = useTheme();

  return (
    <AccentCard railColor={colors.success ?? colors.accent}>
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.sm }}>
        <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flex: 1 }}>
          {pm.businessName}
        </Text>
        <Badge label={pm.status} size="sm" />
      </View>

      <Divider spacing="sm" />

      {/* Caps from the original application */}
      {(pm.requestedPerTradeCapUsd ?? 0) > 0 || (pm.requestedDailyCapUsd ?? 0) > 0 ? (
        <>
          <Text variant="overline" color="textMuted">
            REQUESTED CAPS
          </Text>
          <View style={{ gap: spacing.xs, marginTop: spacing.xs }}>
            {(pm.requestedPerTradeCapUsd ?? 0) > 0 ? (
              <SummaryRow
                label="Per-trade cap"
                value={`$${(pm.requestedPerTradeCapUsd ?? 0).toLocaleString()}`}
              />
            ) : null}
            {(pm.requestedDailyCapUsd ?? 0) > 0 ? (
              <SummaryRow
                label="Daily cap"
                value={`$${(pm.requestedDailyCapUsd ?? 0).toLocaleString()}`}
              />
            ) : null}
          </View>
          <Divider spacing="sm" />
        </>
      ) : null}

      {/* Settlement bank (last 4 digits only for security) */}
      {pm.settlementAccountNumber ? (
        <SummaryRow
          label="Settlement account"
          value={`···${pm.settlementAccountNumber.slice(-4)}`}
        />
      ) : null}

      {/* NGN balance placeholder — requires backend PM-list endpoint */}
      <View
        style={{
          marginTop: spacing.sm,
          backgroundColor: colors.surface,
          borderRadius: 8,
          padding: spacing.sm,
        }}
      >
        <Text variant="caption" color="textMuted">
          NGN balance (available / reserved) is not yet surfaced — add a GET /admin/pm endpoint to the backend to read PaymentMerchant.availableNgn / reservedNgn.
        </Text>
      </View>

      <View style={{ marginTop: spacing.sm }}>
        <Text variant="caption" color="textMuted">
          Approved {formatAge(pm.updatedAt)}
        </Text>
      </View>
    </AccentCard>
  );
};

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

export default PMStatusScreen;
