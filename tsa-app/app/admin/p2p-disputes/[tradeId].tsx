// app/admin/p2p-disputes/[tradeId].tsx
//
// Admin resolve console for a single P2P dispute (keyed by tradeId — the
// resolve endpoints and GetDispute all key off the trade id).
//
// Flow:
//   1. GET /admin/p2p/disputes/:id  → dispute row (reason, evidence, status)
//   2. Admin picks a direction (Release to buyer / Refund to seller) + note
//   3. POST .../resolve              → backend returns { unsignedTx }
//   4. Admin signs + broadcasts via the in-app wallet (same prepare/sign/
//      broadcast/confirm dance the trade screen uses)
//   5. POST .../resolve/confirm      → backend finalizes DB + accounting
//
// The API field `refundBuyer` is a MISNOMER: the Go service packs it verbatim
// as the contract's `adminResolve(tradeId, releaseToBuyer)` bool. So
// refundBuyer:true RELEASES the escrowed crypto to the BUYER, and
// refundBuyer:false REFUNDS it to the seller/funder. We map the radio:
//   'release_buyer' → refundBuyer:true   (release escrow to the buyer)
//   'refund_seller' → refundBuyer:false  (refund escrow to the seller)
//
// Gated to admin / super_admin; non-admins see a clear message instead of a
// confusing 403.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Image,
  Modal,
  Pressable,
  ScrollView,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  BottomActionBar,
  Button,
  Card,
  CopyableField,
  EmptyState,
  ErrorState,
  Icon,
  Input,
  PressableScale,
  RadioGroup,
  Spinner,
  StatusPill,
  SummaryCard,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import {
  adminP2pApi,
  unsignedTxToTxLike,
  type P2PDispute,
} from '../../../services/p2p';
import type { ChainKey } from '../../../constants/chains';
import {
  broadcastTransaction,
  getActiveWallet,
  signTransaction,
} from '../../../services/wallet';
import { useUserRole } from '../../../hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

type Resolution = 'release_buyer' | 'refund_seller';

const ResolveDisputeScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { tradeId } = useLocalSearchParams<{ tradeId: string }>();
  const { role, loading: roleLoading } = useUserRole();
  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const [dispute, setDispute] = useState<P2PDispute | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  const [resolution, setResolution] = useState<Resolution | null>(null);
  const [note, setNote] = useState('');
  const [acting, setActing] = useState<string>(''); // non-empty while signing
  const [viewerUrl, setViewerUrl] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!tradeId || !isAdmin) {
      setLoading(false);
      return;
    }
    setError(false);
    const res = await adminP2pApi.getDispute(String(tradeId));
    if (res.success && res.data) {
      setDispute(res.data);
    } else {
      setError(true);
    }
    setLoading(false);
  }, [tradeId, isAdmin]);

  useEffect(() => {
    if (!roleLoading) load();
  }, [roleLoading, load]);

  const isResolved = dispute?.status === 'resolved' || dispute?.status === 'rejected';

  const handleResolve = useCallback(async () => {
    if (acting || !dispute || !resolution) return;
    const refundBuyer = resolution === 'release_buyer';
    let broadcastHash: string | null = null;
    try {
      setActing('Loading wallet…');
      const adminAddress = await getActiveWallet();
      if (!adminAddress) throw new Error('No wallet found — set up your admin wallet first.');

      setActing('Preparing resolution…');
      const prep = await adminP2pApi.prepareResolve(dispute.tradeId, {
        refundBuyer,
        note: note.trim() || undefined,
        adminAddress,
      });
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');

      setActing('Signing…');
      const signed = await signTransaction(unsignedTxToTxLike(prep.data.unsignedTx), adminAddress);

      setActing('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      setActing('Confirming…');
      const conf = await adminP2pApi.confirmResolve(dispute.tradeId, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');

      toast.success(refundBuyer ? 'Released to buyer' : 'Refunded to seller');
      // Reflect terminal state locally, then return to the queue.
      setDispute((d) => (d ? { ...d, status: 'resolved', refundedToBuyer: refundBuyer } : d));
      router.back();
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        // Tx is on chain but the server confirm failed — DO NOT retry blindly;
        // the chain reconciler will pick it up. Mirror the trade screen's
        // post-broadcast guidance.
        toast.error(
          `Resolution broadcast (tx ${broadcastHash.slice(0, 10)}…) but the server confirm failed. ` +
            `Don’t retry — the reconciler will sync it shortly.`,
        );
      } else {
        toast.error(detail);
      }
    } finally {
      setActing('');
    }
  }, [acting, dispute, resolution, note, toast]);

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

  if (loading || roleLoading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <Spinner label="Loading dispute…" />
        </View>
      </SafeAreaView>
    );
  }

  if (error || !dispute) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <ErrorState
            subtitle="Couldn’t load this dispute."
            onRetry={() => {
              setLoading(true);
              load();
            }}
          />
        </View>
      </SafeAreaView>
    );
  }

  const canSubmit = !!resolution && !acting && !isResolved;
  const evidence = dispute.evidenceUrls ?? [];

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        {/* ── Trade / dispute summary ── */}
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <Text variant="overline" color="textMuted">
              DISPUTE
            </Text>
            <StatusPill kind="dispute" status={dispute.status} size="sm" />
          </View>
          <View style={{ marginTop: spacing.sm }}>
            <SummaryCard>
              <SummaryRow label="Trade ID" value={`#${dispute.tradeId.slice(0, 8)}`} />
              <SummaryRow
                label="Raised by"
                value={dispute.raisedRole === 'buyer' ? 'Buyer' : 'Seller'}
              />
              <SummaryRow label="Filed" value={formatDateTime(dispute.createdAt)} />
            </SummaryCard>
          </View>
        </Card>

        {/* ── Reason ── */}
        <AccentCard railColor={dispute.status === 'open' ? colors.danger : colors.accentRail} style={{ marginTop: spacing.md }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Icon name="dispute" size={18} color={dispute.status === 'open' ? colors.danger : colors.accent} />
            <Text variant="bodyMed" weight="700">
              Reason from the {dispute.raisedRole}
            </Text>
          </View>
          <Text variant="body" color="textSecondary" style={{ marginTop: spacing.sm }}>
            {dispute.reason?.trim() || 'No reason was provided.'}
          </Text>
        </AccentCard>

        {/* ── Evidence ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.sm }}>
          Evidence
        </Text>
        {evidence.length === 0 ? (
          <Card>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="image" size={18} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary">
                No images were attached to this dispute.
              </Text>
            </View>
          </Card>
        ) : (
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
            {evidence.map((url, i) => (
              <PressableScale key={`${url}-${i}`} onPress={() => setViewerUrl(url)}>
                <Image
                  source={{ uri: url }}
                  style={{
                    width: 104,
                    height: 104,
                    borderRadius: 12,
                    backgroundColor: colors.surfaceAlt,
                  }}
                />
              </PressableScale>
            ))}
          </View>
        )}

        {/* ── Resolution form (hidden once terminal) ── */}
        {isResolved ? (
          <AccentCard railColor={colors.success} style={{ marginTop: spacing.xl }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="checkCircle" size={18} color={colors.success} />
              <Text variant="bodyMed" weight="700">
                Already resolved
              </Text>
            </View>
            {dispute.refundedToBuyer != null && (
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                Outcome: {dispute.refundedToBuyer ? 'Released to buyer' : 'Refunded to seller'}.
              </Text>
            )}
            {dispute.resolution ? (
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                Note: {dispute.resolution}
              </Text>
            ) : null}
            {dispute.resolveTxHash ? (
              <View style={{ marginTop: spacing.md }}>
                <CopyableField label="Resolution tx" value={dispute.resolveTxHash} />
              </View>
            ) : null}
          </AccentCard>
        ) : (
          <>
            <Text variant="title3" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.sm }}>
              Resolution
            </Text>
            <RadioGroup<Resolution>
              options={[
                {
                  value: 'release_buyer',
                  label: 'Release to buyer',
                  sublabel: 'Buyer paid — release the escrowed crypto to them.',
                  icon: 'checkCircle',
                },
                {
                  value: 'refund_seller',
                  label: 'Refund to seller',
                  sublabel: 'Buyer did not pay — return the escrow to the seller.',
                  icon: 'refresh',
                },
              ]}
              value={resolution}
              onChange={setResolution}
              disabled={!!acting}
            />

            <View style={{ marginTop: spacing.lg }}>
              <Input
                label="Resolution note (optional)"
                placeholder="Record why you decided this — visible in the audit log."
                value={note}
                onChangeText={setNote}
                multiline
                numberOfLines={4}
                maxLength={1000}
                counter
                editable={!acting}
                textAlignVertical="top"
                inputStyle={{ minHeight: 92 }}
              />
            </View>

            <Card style={{ marginTop: spacing.lg }}>
              <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                <Icon name="info" size={16} color={colors.info} />
                <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                  Resolving moves escrowed funds on chain. You’ll sign the
                  transaction with your admin wallet — this can’t be undone.
                </Text>
              </View>
            </Card>
          </>
        )}
      </ScrollView>

      {!isResolved && (
        <BottomActionBar>
          {acting ? (
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.sm, paddingVertical: spacing.sm }}>
              <ActivityIndicator color={colors.accent} />
              <Text variant="bodyMed" weight="600" color="textSecondary">
                {acting}
              </Text>
            </View>
          ) : (
            <Button
              title={
                resolution === 'refund_seller'
                  ? 'Refund to seller'
                  : resolution === 'release_buyer'
                  ? 'Release to buyer'
                  : 'Resolve dispute'
              }
              rightIcon="shield"
              variant={resolution === 'refund_seller' ? 'danger' : 'primary'}
              disabled={!canSubmit}
              onPress={handleResolve}
            />
          )}
        </BottomActionBar>
      )}

      {/* Full-screen evidence viewer */}
      <Modal visible={!!viewerUrl} transparent animationType="fade" onRequestClose={() => setViewerUrl(null)}>
        <Pressable
          onPress={() => setViewerUrl(null)}
          style={{ flex: 1, backgroundColor: 'rgba(0,0,0,0.9)', alignItems: 'center', justifyContent: 'center', padding: spacing.lg }}
        >
          {viewerUrl && (
            <Image source={{ uri: viewerUrl }} style={{ width: '100%', height: '80%' }} resizeMode="contain" />
          )}
          <View style={{ position: 'absolute', top: spacing['3xl'], right: spacing.lg }}>
            <Badge label="Close" icon="close" />
          </View>
        </Pressable>
      </Modal>
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

export default ResolveDisputeScreen;
