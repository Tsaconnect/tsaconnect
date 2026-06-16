// app/admin/pm-queue/[tradeId].tsx
//
// PM manual-release detail screen. The operator lands here after tapping a row
// in the PM queue. Trade context (buyerName, amounts, token, createdAt) is
// passed as route params from the list screen so we don't need an additional
// API call.
//
// Two actions:
//   Release — operator has verified the bank transfer landed in the PM's Nomba
//             account. Calls pmAdminApi.release() → crypto goes to the buyer.
//   Decline — trade is flagged for cancellation on timeout; no immediate refund.
//             Calls pmAdminApi.decline() → the timeout watchdog will cancel and
//             return escrow on its next tick.
//
// Error codes:
//   404 — trade vanished (already processed by another operator)
//   409 — trade already handled
//   403 — caller lacks PM operator permissions
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  BottomActionBar,
  Button,
  Card,
  EmptyState,
  Icon,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import { pmAdminApi } from '../../../services/p2p';
import { useUserRole } from '../../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

type Params = {
  tradeId: string;
  buyerName?: string;
  token?: string;
  tokenAmount?: string;
  fiatAmount?: string;
  createdAt?: string;
};

function errorMessage(status?: number): string {
  if (status === 404) return 'Trade not found';
  if (status === 409) return 'Trade already handled';
  if (status === 403) return 'Not authorized for PM operations';
  return 'Something went wrong. Please try again.';
}

const PMTradeDetailScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { role, loading: roleLoading } = useUserRole();

  const {
    tradeId,
    buyerName,
    token,
    tokenAmount,
    fiatAmount,
    createdAt,
  } = useLocalSearchParams<Params>();

  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const [acting, setActing] = useState<'release' | 'decline' | null>(null);

  const handleRelease = useCallback(() => {
    Alert.alert(
      'Release to buyer?',
      "Only release after you've verified this exact payment landed in the PM's Nomba account. This sends crypto to the buyer.",
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Release',
          style: 'default',
          onPress: async () => {
            if (!tradeId || acting) return;
            setActing('release');
            try {
              const res = await pmAdminApi.release(String(tradeId));
              if (res.success) {
                toast.success('Released — crypto sent to buyer');
                router.back();
              } else {
                toast.error(errorMessage(res.status));
              }
            } catch {
              toast.error('Something went wrong. Please try again.');
            } finally {
              setActing(null);
            }
          },
        },
      ],
    );
  }, [tradeId, acting, toast]);

  const handleDecline = useCallback(() => {
    Alert.alert(
      'Decline this trade?',
      "Decline this trade? It will be cancelled automatically on timeout and the buyer's deposit (if any) refunded.",
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Decline',
          style: 'destructive',
          onPress: async () => {
            if (!tradeId || acting) return;
            setActing('decline');
            try {
              const res = await pmAdminApi.decline(String(tradeId));
              if (res.success) {
                toast.success('Declined — will be cancelled on timeout');
                router.back();
              } else {
                toast.error(errorMessage(res.status));
              }
            } catch {
              toast.error('Something went wrong. Please try again.');
            } finally {
              setActing(null);
            }
          },
        },
      ],
    );
  }, [tradeId, acting, toast]);

  // ─── Role gate ──────────────────────────────────────────────────────────
  if (!roleLoading && !isAdmin) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="lock"
            title="Admins only"
            subtitle="PM trade review is restricted to admin and super-admin accounts."
            actionLabel="Go back"
            onAction={() => router.back()}
          />
        </View>
      </SafeAreaView>
    );
  }

  if (roleLoading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner label="Loading…" />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>

        {/* ── Trade summary ── */}
        <Card>
          <Text variant="overline" color="textMuted">
            PM TRADE
          </Text>
          <View style={{ marginTop: spacing.sm }}>
            <SummaryCard>
              <SummaryRow label="Trade ID" value={`#${String(tradeId || '').slice(0, 8)}`} />
              <SummaryRow label="Buyer" value={buyerName || '—'} />
              {token && tokenAmount ? (
                <SummaryRow label="Crypto amount" value={`${tokenAmount} ${token}`} />
              ) : null}
              {fiatAmount ? (
                <SummaryRow label="Fiat amount" value={`NGN ${fiatAmount}`} />
              ) : null}
              {createdAt ? (
                <SummaryRow label="Created" value={formatDateTime(String(createdAt))} />
              ) : null}
            </SummaryCard>
          </View>
        </Card>

        {/* ── Amounts ── */}
        {tokenAmount && token ? (
          <AccentCard railColor={colors.warning} style={{ marginTop: spacing.md }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="warning" size={18} color={colors.warning} />
              <Text variant="bodyMed" weight="700">
                Awaiting operator verification
              </Text>
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.md }}>
              <View>
                <Text variant="overline" color="textMuted">
                  CRYPTO TO RELEASE
                </Text>
                <Text variant="title3" weight="700" numeric style={{ marginTop: 2 }}>
                  {tokenAmount} {token}
                </Text>
              </View>
              {fiatAmount ? (
                <View style={{ alignItems: 'flex-end' }}>
                  <Text variant="overline" color="textMuted">
                    FIAT EXPECTED
                  </Text>
                  <AmountText
                    value={fiatAmount}
                    currency="NGN"
                    variant="title3"
                    weight="700"
                    style={{ marginTop: 2 }}
                  />
                </View>
              ) : null}
            </View>
          </AccentCard>
        ) : null}

        {/* ── Guidance ── */}
        <Card style={{ marginTop: spacing.md }}>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            <Icon name="info" size={16} color={colors.info} />
            <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
              {"Check the PM's Nomba inbound account to confirm the buyer's transfer matches the fiat amount above before releasing. If the payment is missing or the amounts don't match, decline — the trade will be cancelled by the timeout watchdog."}
            </Text>
          </View>
        </Card>
      </ScrollView>

      <BottomActionBar>
        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <View style={{ flex: 1 }}>
            <Button
              title={acting === 'decline' ? 'Declining…' : 'Decline'}
              variant="danger"
              disabled={!!acting}
              onPress={handleDecline}
            />
          </View>
          <View style={{ flex: 1 }}>
            <Button
              title={acting === 'release' ? 'Releasing…' : 'Release'}
              variant="primary"
              disabled={!!acting}
              onPress={handleRelease}
            />
          </View>
        </View>
      </BottomActionBar>
    </SafeAreaView>
  );
};

function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export default PMTradeDetailScreen;
