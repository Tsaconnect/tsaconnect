// app/admin/pm-applications.tsx
//
// Admin screen: list pending p2p_merchant MerchantRequests and approve them.
// Approving triggers backend provisioning of a Nomba sub-account + VA and
// activates the PM. The backend may return 5xx on transient Nomba failure —
// the action can be retried.
//
// Pattern mirrors app/admin/p2p-disputes.tsx:
//   - role gate via useUserRole + ADMIN_ROLES
//   - load on focus (useFocusEffect), pull-to-refresh
//   - loading/empty/error states
//   - confirm dialog before action (Alert.alert)
//   - toast on success/failure (useToast)
//
// API: GET /admin/merchant-requests?status=pending&limit=100
//       (filtered to businessType === 'p2p_merchant' in pmAdminApi.listApplications)
//       POST /admin/merchant-requests/:id/approve
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  SkeletonGroup,
  Text,
  useToast,
} from '@/components/ui';
import { pmAdminApi, type MerchantRequest } from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

const PMApplicationsScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { role, loading: roleLoading } = useUserRole();

  const [applications, setApplications] = useState<MerchantRequest[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  const [approving, setApproving] = useState<string | null>(null);

  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const load = useCallback(async () => {
    if (!isAdmin) {
      setLoading(false);
      return;
    }
    setError(false);
    const res = await pmAdminApi.listApplications();
    if (res.success && res.data) {
      setApplications(res.data.requests);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [isAdmin]);

  // Reload on focus so approved rows drop out of the queue when the admin
  // navigates back from another screen.
  useFocusEffect(
    useCallback(() => {
      if (!roleLoading) {
        setLoading(true);
        load();
      }
    }, [roleLoading, load]),
  );

  const handleApprove = useCallback(
    (app: MerchantRequest) => {
      Alert.alert(
        'Approve PM application?',
        `Approving "${app.businessName}" provisions a Nomba sub-account + virtual account and activates the PM. This action can be retried on transient failure.`,
        [
          { text: 'Cancel', style: 'cancel' },
          {
            text: 'Approve',
            style: 'default',
            onPress: async () => {
              setApproving(app.id);
              const res = await pmAdminApi.approveApplication(app.id);
              setApproving(null);
              if (res.success) {
                toast.success(`${app.businessName} approved and activated`);
                // Remove from local list optimistically; pull-to-refresh syncs
                setApplications((prev) => prev.filter((a) => a.id !== app.id));
              } else {
                // 5xx provisioning errors are retry-able — surface the backend
                // message so the operator knows whether it's transient.
                const msg = res.message ?? 'Approval failed. Please try again.';
                toast.error(msg);
              }
            },
          },
        ],
      );
    },
    [toast],
  );

  // ─── Role gate ──────────────────────────────────────────────────────────
  if (!roleLoading && !isAdmin) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="lock"
            title="Admins only"
            subtitle="PM application approval is restricted to admin and super-admin accounts."
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
          PM Applications
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
          Pending corporate Payment Merchant applications. Approving provisions a Nomba sub-account + VA.
        </Text>

        <View style={{ marginTop: spacing.sm }}>
          {loading || roleLoading ? (
            <SkeletonGroup shape="tradeRow" count={4} />
          ) : error ? (
            <ErrorState
              subtitle="Couldn't load PM applications. Check your connection."
              onRetry={() => {
                setLoading(true);
                load();
              }}
            />
          ) : applications.length === 0 ? (
            <EmptyState
              icon="checkCircle"
              title="No pending applications"
              subtitle="Pending p2p_merchant corporate applications will appear here. Pull to refresh."
            />
          ) : (
            <View style={{ gap: spacing.md }}>
              {applications.map((app) => (
                <ApplicationRow
                  key={app.id}
                  application={app}
                  approving={approving === app.id}
                  onApprove={() => handleApprove(app)}
                />
              ))}
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

interface ApplicationRowProps {
  application: MerchantRequest;
  approving: boolean;
  onApprove: () => void;
}

const ApplicationRow: React.FC<ApplicationRowProps> = ({ application, approving, onApprove }) => {
  const { colors, spacing } = useTheme();

  const capLabel =
    application.requestedPerTradeCapUsd && application.requestedPerTradeCapUsd > 0
      ? `$${application.requestedPerTradeCapUsd.toLocaleString()} / trade`
      : null;

  const dailyCapLabel =
    application.requestedDailyCapUsd && application.requestedDailyCapUsd > 0
      ? `$${application.requestedDailyCapUsd.toLocaleString()} / day`
      : null;

  return (
    <AccentCard railColor={colors.accent}>
      <View style={{ flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between', gap: spacing.sm }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}>
          <Icon name="user" size={20} color={colors.accent} />
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="700" numberOfLines={1}>
              {application.businessName}
            </Text>
            <Text variant="caption" color="textMuted">
              {application.user?.username
                ? `@${application.user.username}`
                : application.user?.email ?? shortId(application.userId)}
              {' · '}
              {formatAge(application.createdAt)}
            </Text>
          </View>
        </View>
        <Badge label="Pending" size="sm" />
      </View>

      {application.businessDescription ? (
        <>
          <Divider spacing="sm" />
          <Text variant="overline" color="textMuted">
            DESCRIPTION
          </Text>
          <Text variant="bodySm" numberOfLines={3} style={{ marginTop: 2 }}>
            {application.businessDescription}
          </Text>
        </>
      ) : null}

      <Divider spacing="sm" />

      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs, marginBottom: spacing.sm }}>
        {application.settlementAccountNumber ? (
          <Badge
            label={`Settlement: ···${application.settlementAccountNumber.slice(-4)}`}
            icon="info"
            size="sm"
          />
        ) : null}
        {capLabel ? <Badge label={capLabel} size="sm" /> : null}
        {dailyCapLabel ? <Badge label={dailyCapLabel} size="sm" /> : null}
        {application.makerWalletAddress ? (
          <Badge label={`Wallet: ${shortId(application.makerWalletAddress)}`} size="sm" />
        ) : null}
      </View>

      <Button
        variant="primary"
        size="sm"
        title={approving ? 'Approving…' : 'Approve'}
        onPress={onApprove}
        disabled={approving}
      />
    </AccentCard>
  );
};

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

export default PMApplicationsScreen;
