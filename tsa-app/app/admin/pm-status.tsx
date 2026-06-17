// app/admin/pm-status.tsx
//
// PM status view — shows all PaymentMerchant rows with status, VA / inbound
// account number, available + reserved NGN balances, and trade caps.
//
// Data source: GET /admin/pm (PMHandler.ListPMs — safe projection, no secrets).
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
  AmountText,
  Badge,
  Divider,
  EmptyState,
  ErrorState,
  SkeletonGroup,
  SummaryRow,
  Text,
} from '@/components/ui';
import { pmAdminApi, type PaymentMerchant } from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

const PMStatusScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { role, loading: roleLoading } = useUserRole();

  const [pms, setPMs] = useState<PaymentMerchant[]>([]);
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
    const res = await pmAdminApi.listPMs();
    if (res.success && res.data) {
      setPMs(res.data);
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
          All Payment Merchants — live NGN balances, caps, and account details.
        </Text>

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
          ) : pms.length === 0 ? (
            <EmptyState
              icon="checkCircle"
              title="No payment merchants yet"
              subtitle="Payment merchants will appear here once provisioned."
            />
          ) : (
            <View style={{ gap: spacing.md }}>
              {pms.map((pm) => (
                <PMStatusCard key={pm.id} pm={pm} />
              ))}
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const PMStatusCard: React.FC<{ pm: PaymentMerchant }> = ({ pm }) => {
  const { colors, spacing } = useTheme();

  const railColor =
    pm.status === 'active'
      ? (colors.success ?? colors.accent)
      : pm.status === 'paused'
        ? (colors.warning ?? colors.textSecondary)
        : colors.textSecondary;

  return (
    <AccentCard railColor={railColor}>
      {/* Header: name + status badge */}
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.sm }}>
        <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flex: 1 }}>
          {pm.name}
        </Text>
        <Badge label={pm.status} size="sm" />
      </View>

      <Divider spacing="sm" />

      {/* NGN balances */}
      <Text variant="overline" color="textMuted">
        NGN BALANCES
      </Text>
      <View style={{ gap: spacing.xs, marginTop: spacing.xs }}>
        <SummaryRow
          label="Available"
          value={<AmountText value={pm.availableNgn} currency="NGN" variant="bodySm" weight="600" />}
        />
        <SummaryRow
          label="Reserved"
          value={<AmountText value={pm.reservedNgn} currency="NGN" variant="bodySm" weight="600" />}
        />
      </View>

      <Divider spacing="sm" />

      {/* Account details */}
      <Text variant="overline" color="textMuted">
        ACCOUNT
      </Text>
      <View style={{ gap: spacing.xs, marginTop: spacing.xs }}>
        {pm.inboundAccountNumber ? (
          <SummaryRow label="VA / inbound account" value={pm.inboundAccountNumber} />
        ) : null}
        {pm.nombaWalletId ? (
          <SummaryRow label="Nomba wallet ID" value={pm.nombaWalletId} />
        ) : null}
        <SummaryRow label="Provisioning" value={pm.provisioningStatus} />
      </View>

      {/* Caps */}
      {(pm.perTradeCapUsd ?? 0) > 0 || (pm.dailyCapUsd ?? 0) > 0 ? (
        <>
          <Divider spacing="sm" />
          <Text variant="overline" color="textMuted">
            CAPS
          </Text>
          <View style={{ gap: spacing.xs, marginTop: spacing.xs }}>
            {(pm.perTradeCapUsd ?? 0) > 0 ? (
              <SummaryRow
                label="Per-trade cap"
                value={`$${(pm.perTradeCapUsd ?? 0).toLocaleString()}`}
              />
            ) : null}
            {(pm.dailyCapUsd ?? 0) > 0 ? (
              <SummaryRow
                label="Daily cap"
                value={`$${(pm.dailyCapUsd ?? 0).toLocaleString()}`}
              />
            ) : null}
          </View>
        </>
      ) : null}

      {/* Settlement bank (last 4 digits only) */}
      {pm.settlementAccountNumber ? (
        <>
          <Divider spacing="sm" />
          <SummaryRow
            label="Settlement account"
            value={`···${pm.settlementAccountNumber.slice(-4)}`}
          />
        </>
      ) : null}
    </AccentCard>
  );
};

export default PMStatusScreen;
