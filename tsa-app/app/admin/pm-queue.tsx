// app/admin/pm-queue.tsx
//
// Admin PM manual-release queue — lists trades the PM automation flagged for
// human review (name / amount mismatch on inbound bank transfer). Gated to
// admin / super_admin; a 403 from the backend means the operator lacks the
// PM role and gets a specific message instead of a generic error.
//
// Each row shows: buyer name, token amount, fiat amount, and relative age.
// Tapping navigates to the detail screen where the operator can release or
// decline the trade.
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Badge,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  SkeletonGroup,
  Text,
} from '@/components/ui';
import { pmAdminApi, type PMManualQueueItem } from '../../services/p2p';
import { useUserRole } from '../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

const PMQueueScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { role, loading: roleLoading } = useUserRole();
  const [items, setItems] = useState<PMManualQueueItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<'generic' | '403' | null>(null);

  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const load = useCallback(async () => {
    if (!isAdmin) {
      setLoading(false);
      return;
    }
    setError(null);
    const res = await pmAdminApi.manualQueue();
    if (res.success && res.data) {
      setItems(res.data);
    } else if (res.status === 403) {
      setError('403');
    } else {
      setError('generic');
    }
    setLoading(false);
    setRefreshing(false);
  }, [isAdmin]);

  // Reload on focus so items resolved on the detail screen drop out of the
  // queue when the operator navigates back.
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
            subtitle="The PM manual queue is restricted to admin and super-admin accounts."
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
          Manual Release Queue
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
          {"Trades the PM bot couldn't auto-match. Verify the bank transfer then release or decline."}
        </Text>

        <View style={{ marginTop: spacing.sm }}>
          {loading || roleLoading ? (
            <SkeletonGroup shape="tradeRow" count={5} />
          ) : error === '403' ? (
            <EmptyState
              icon="lock"
              title="Not authorized for PM operations"
              subtitle="You're not authorized for PM operations. Contact a super-admin."
            />
          ) : error === 'generic' ? (
            <ErrorState
              subtitle="Couldn't load the PM queue. Check your connection."
              onRetry={() => {
                setLoading(true);
                load();
              }}
            />
          ) : items.length === 0 ? (
            <EmptyState
              icon="checkCircle"
              title="Nothing awaiting review"
              subtitle="PM trades that need manual verification will appear here. Pull to refresh."
            />
          ) : (
            <View style={{ gap: spacing.md }}>
              {items.map((item) => (
                <QueueRow key={item.tradeId} item={item} />
              ))}
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const QueueRow: React.FC<{ item: PMManualQueueItem }> = ({ item }) => {
  const { colors, spacing } = useTheme();

  const handlePress = () => {
    router.push({
      pathname: '/admin/pm-queue/[tradeId]' as any,
      params: {
        tradeId: item.tradeId,
        buyerName: item.buyerName,
        token: item.token,
        tokenAmount: item.tokenAmount,
        fiatAmount: item.fiatAmount,
        createdAt: item.createdAt,
      },
    });
  };

  return (
    <PressableScale onPress={handlePress}>
      <AccentCard railColor={colors.warning}>
        <View style={{ flexDirection: 'row', alignItems: 'flex-start', justifyContent: 'space-between', gap: spacing.sm }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}>
            <Icon name="user" size={20} color={colors.warning} />
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                {item.buyerName || 'Unknown buyer'}
              </Text>
              <Text variant="caption" color="textMuted">
                Trade #{shortId(item.tradeId)} · {formatAge(item.createdAt)}
              </Text>
            </View>
          </View>
          <Icon name="chevronRight" size={18} color={colors.textMuted} />
        </View>

        <Divider spacing="sm" />

        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <View style={{ gap: 2 }}>
            <Text variant="overline" color="textMuted">
              CRYPTO
            </Text>
            <Text variant="bodyMed" weight="700" numeric>
              {item.tokenAmount} {item.token}
            </Text>
          </View>
          <View style={{ gap: 2, alignItems: 'flex-end' }}>
            <Text variant="overline" color="textMuted">
              FIAT
            </Text>
            <AmountText value={item.fiatAmount} currency="NGN" variant="bodyMed" weight="700" />
          </View>
        </View>

        <View style={{ marginTop: spacing.sm }}>
          <Badge label="Awaiting review" icon="info" size="sm" />
        </View>
      </AccentCard>
    </PressableScale>
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

export default PMQueueScreen;
