// app/admin/p2p-disputes.tsx
//
// Admin queue for P2P trade disputes. Each card shows the dispute summary;
// tapping "Resolve" opens a modal where the admin chooses buyer/seller +
// optional note, then signs the on-chain adminResolve tx (the backend
// expects prepare → sign → broadcast → confirm).
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Modal,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import type { ChainKey } from '../../constants/chains';
import { adminP2pApi, type P2PDispute, type UnsignedTx } from '../../services/p2p';
import { broadcastTransaction, getActiveWallet, signTransaction } from '../../services/wallet';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#28A745',
  error: '#DC3545',
  info: '#3B82F6',
};

type StatusFilter = 'open' | 'resolved' | 'all';

const AdminDisputesScreen: React.FC = () => {
  const [filter, setFilter] = useState<StatusFilter>('open');
  const [disputes, setDisputes] = useState<P2PDispute[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [resolveTarget, setResolveTarget] = useState<P2PDispute | null>(null);

  const load = useCallback(async () => {
    const status = filter === 'all' ? undefined : filter;
    const res = await adminP2pApi.listDisputes(status);
    if (res.success && res.data) setDisputes(res.data);
    setLoading(false);
    setRefreshing(false);
  }, [filter]);

  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        contentContainerStyle={styles.scroll}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text style={styles.heading}>P2P Disputes</Text>
        <Text style={styles.subheading}>
          Choose buyer or seller and sign the resolve transaction. Funds remain
          escrowed on chain until the receipt is recorded.
        </Text>

        <View style={styles.filterRow}>
          {(['open', 'resolved', 'all'] as StatusFilter[]).map((s) => (
            <TouchableOpacity
              key={s}
              style={[styles.filter, filter === s && styles.filterActive]}
              onPress={() => setFilter(s)}
              activeOpacity={0.85}
            >
              <Text style={[styles.filterText, filter === s && styles.filterTextActive]}>
                {s[0].toUpperCase() + s.slice(1)}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : disputes.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="gavel" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              {filter === 'open'
                ? 'No open disputes right now.'
                : `No disputes in '${filter}' status.`}
            </Text>
          </View>
        ) : (
          disputes.map((d) => (
            <DisputeCard
              key={d.id}
              dispute={d}
              onResolve={() => setResolveTarget(d)}
              onViewTrade={() => router.push(`/p2p/trade/${d.tradeId}`)}
            />
          ))
        )}
      </ScrollView>

      <ResolveModal
        dispute={resolveTarget}
        onClose={() => setResolveTarget(null)}
        onResolved={() => {
          setResolveTarget(null);
          load();
        }}
      />
    </SafeAreaView>
  );
};

const DisputeCard: React.FC<{
  dispute: P2PDispute;
  onResolve: () => void;
  onViewTrade: () => void;
}> = ({ dispute, onResolve, onViewTrade }) => {
  const isOpen = dispute.status === 'open';
  return (
    <View style={styles.card}>
      <View style={styles.cardTop}>
        <Text style={styles.cardTitle}>
          {dispute.raisedRole === 'buyer' ? 'Buyer-raised' : 'Seller-raised'}
        </Text>
        <View style={[styles.statusBadge, isOpen ? styles.badgeOpen : styles.badgeDone]}>
          <Text style={[styles.statusBadgeText, !isOpen && { color: '#FFF' }]}>
            {dispute.status}
          </Text>
        </View>
      </View>
      <Text style={styles.cardReason} numberOfLines={4}>
        {dispute.reason || '(no reason given)'}
      </Text>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Trade</Text>
        <Text style={styles.cardMetaValue}>•{dispute.tradeId.slice(0, 10)}</Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Raised</Text>
        <Text style={styles.cardMetaValue}>{formatRelative(dispute.createdAt)}</Text>
      </View>
      {dispute.resolvedAt && (
        <View style={styles.cardMetaRow}>
          <Text style={styles.cardMetaLabel}>Resolved</Text>
          <Text style={styles.cardMetaValue}>
            {dispute.refundedToBuyer ? 'to buyer' : 'to seller'}
            {' • '}
            {formatRelative(dispute.resolvedAt)}
          </Text>
        </View>
      )}

      <View style={styles.cardActions}>
        <TouchableOpacity
          style={styles.secondaryBtn}
          onPress={onViewTrade}
          activeOpacity={0.85}
        >
          <Icon name="open-in-new" size={16} color={GOLD.dark} />
          <Text style={styles.secondaryBtnText}>Open trade</Text>
        </TouchableOpacity>
        {isOpen && (
          <TouchableOpacity
            style={styles.primaryBtn}
            onPress={onResolve}
            activeOpacity={0.85}
          >
            <Icon name="gavel" size={16} color="#000" />
            <Text style={styles.primaryBtnText}>Resolve</Text>
          </TouchableOpacity>
        )}
      </View>
    </View>
  );
};

const ResolveModal: React.FC<{
  dispute: P2PDispute | null;
  onClose: () => void;
  onResolved: () => void;
}> = ({ dispute, onClose, onResolved }) => {
  const [refundBuyer, setRefundBuyer] = useState<boolean | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);

  // Clear selection whenever the modal opens or closes. Keying on
  // `dispute?.id` would let a stale 'refund buyer' choice persist if
  // an admin cancels and re-opens the same dispute — a footgun for a
  // wrong-button resolution.
  React.useEffect(() => {
    setRefundBuyer(null);
    setNote('');
  }, [dispute]);

  if (!dispute) return null;

  const handleClose = () => {
    if (busy) return;
    setRefundBuyer(null);
    setNote('');
    onClose();
  };

  const submit = async () => {
    if (refundBuyer === null || busy) return;
    let broadcastHash: string | null = null;
    try {
      setBusy(true);
      const adminAddress = await getActiveWallet();
      if (!adminAddress) throw new Error('No wallet found');

      const prep = await adminP2pApi.prepareResolve(dispute.tradeId, {
        refundBuyer,
        note: note.trim() || undefined,
        adminAddress,
      });
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');

      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      const conf = await adminP2pApi.confirmResolve(dispute.tradeId, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
      onResolved();
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        Alert.alert(
          'Resolve submitted, server confirm failed',
          `Tx ${broadcastHash} was broadcast on chain but the server couldn't record it ` +
            `(${detail}). Wait a minute for the reconciler to catch up.`,
        );
      } else {
        Alert.alert('Resolve failed', detail);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal animationType="slide" transparent visible={!!dispute} onRequestClose={handleClose}>
      <View style={styles.modalOverlay}>
        <View style={styles.modalCard}>
          <Text style={styles.modalTitle}>Resolve dispute</Text>
          <Text style={styles.modalBody}>
            Choose who receives the escrowed funds. Both options sign the
            on-chain adminResolve transaction.
          </Text>

          <View style={styles.choiceRow}>
            <TouchableOpacity
              style={[styles.choice, refundBuyer === true && styles.choiceActive]}
              onPress={() => setRefundBuyer(true)}
              disabled={busy}
              activeOpacity={0.85}
            >
              <Icon
                name="person"
                size={18}
                color={refundBuyer === true ? '#000' : GOLD.dark}
              />
              <Text
                style={[styles.choiceText, refundBuyer === true && styles.choiceTextActive]}
              >
                Refund buyer
              </Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.choice, refundBuyer === false && styles.choiceActive]}
              onPress={() => setRefundBuyer(false)}
              disabled={busy}
              activeOpacity={0.85}
            >
              <Icon
                name="storefront"
                size={18}
                color={refundBuyer === false ? '#000' : GOLD.dark}
              />
              <Text
                style={[styles.choiceText, refundBuyer === false && styles.choiceTextActive]}
              >
                Release to seller
              </Text>
            </TouchableOpacity>
          </View>

          <TextInput
            style={styles.modalInput}
            placeholder="Internal note (optional, persists in audit log)"
            placeholderTextColor="#999"
            value={note}
            onChangeText={setNote}
            multiline
            maxLength={500}
            editable={!busy}
          />

          <View style={styles.modalActions}>
            <TouchableOpacity
              style={styles.modalCancel}
              onPress={handleClose}
              disabled={busy}
            >
              <Text style={styles.modalCancelText}>Cancel</Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.modalSubmit, (refundBuyer === null || busy) && styles.btnDisabled]}
              onPress={submit}
              disabled={refundBuyer === null || busy}
            >
              <Text style={styles.modalSubmitText}>
                {busy ? 'Working…' : 'Sign & resolve'}
              </Text>
            </TouchableOpacity>
          </View>
        </View>
      </View>
    </Modal>
  );
};

function toTxLike(tx: UnsignedTx) {
  const ensure0x = (v?: string) => (!v ? undefined : v.startsWith('0x') ? v : `0x${v}`);
  return {
    to: tx.to,
    value: tx.value || '0',
    data: ensure0x(tx.data),
    nonce: tx.nonce,
    gasLimit: tx.gasLimit,
    gasPrice: tx.gasPrice,
    chainId: tx.chainId ? Number(tx.chainId) : undefined,
  };
}

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return iso;
  if (ms < 60_000) return 'just now';
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h ago`;
  return `${Math.floor(ms / 86_400_000)}d ago`;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000' },
  subheading: { fontSize: 13, color: '#666', marginTop: 4, marginBottom: 16, lineHeight: 18 },
  filterRow: { flexDirection: 'row', gap: 10, marginBottom: 14 },
  filter: {
    paddingVertical: 6,
    paddingHorizontal: 12,
    borderRadius: 16,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  filterActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  filterText: { fontSize: 12, color: GOLD.dark, fontWeight: '700' },
  filterTextActive: { color: '#000' },
  empty: { alignItems: 'center', paddingVertical: 60 },
  emptyText: { marginTop: 12, fontSize: 14, color: '#888', textAlign: 'center', maxWidth: 280 },
  card: {
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 16,
    marginBottom: 12,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.06,
    shadowRadius: 4,
    elevation: 2,
  },
  cardTop: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  cardTitle: { fontSize: 16, fontWeight: '700', color: '#000' },
  cardReason: { fontSize: 13, color: '#333', marginBottom: 8, lineHeight: 18 },
  statusBadge: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: 6 },
  badgeOpen: { backgroundColor: '#FBE9E7', borderWidth: 1, borderColor: GOLD.error },
  badgeDone: { backgroundColor: GOLD.success },
  statusBadgeText: { fontSize: 11, fontWeight: '700', textTransform: 'capitalize', color: GOLD.error },
  cardMetaRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 3 },
  cardMetaLabel: { fontSize: 13, color: '#666' },
  cardMetaValue: { fontSize: 13, color: '#000', fontWeight: '600' },
  cardActions: { flexDirection: 'row', gap: 10, marginTop: 12 },
  secondaryBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD.dark,
  },
  secondaryBtnText: { fontSize: 13, color: GOLD.dark, fontWeight: '700' },
  primaryBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: 8,
    backgroundColor: GOLD.primary,
  },
  primaryBtnText: { fontSize: 13, color: '#000', fontWeight: '800' },

  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.6)',
    justifyContent: 'center',
    paddingHorizontal: 20,
  },
  modalCard: {
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 18,
  },
  modalTitle: { fontSize: 18, fontWeight: '800', color: '#000', marginBottom: 6 },
  modalBody: { fontSize: 13, color: '#555', marginBottom: 14, lineHeight: 18 },
  choiceRow: { flexDirection: 'row', gap: 10, marginBottom: 12 },
  choice: {
    flex: 1,
    flexDirection: 'row',
    gap: 6,
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 10,
    borderRadius: 8,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  choiceActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  choiceText: { fontWeight: '700', color: GOLD.dark, fontSize: 13 },
  choiceTextActive: { color: '#000' },
  modalInput: {
    backgroundColor: '#FAFAFA',
    borderRadius: 8,
    padding: 12,
    minHeight: 80,
    textAlignVertical: 'top',
    fontSize: 14,
    color: '#000',
    borderWidth: 1,
    borderColor: '#EEE',
    marginBottom: 12,
  },
  modalActions: { flexDirection: 'row', justifyContent: 'flex-end', gap: 10 },
  modalCancel: { paddingVertical: 8, paddingHorizontal: 14 },
  modalCancelText: { color: GOLD.dark, fontWeight: '700' },
  modalSubmit: {
    backgroundColor: GOLD.primary,
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 8,
  },
  modalSubmitText: { color: '#000', fontWeight: '800' },
  btnDisabled: { opacity: 0.4 },
});

export default AdminDisputesScreen;
