// app/p2p/trade/[tradeId].tsx
//
// The state-machine cockpit for a single P2P trade. Single screen that:
//
//   - Renders the trade's current status as a visual progress bar
//   - Shows trade summary (counterparty, amount, rate, payment method)
//   - Surfaces context-aware actions per role + status:
//       buyer / pending_payment    → Mark as paid (with proof upload)
//       seller / pending or paid   → Release
//       either / paid              → Open dispute
//       either / past expiry       → Cancel (anyone after 30 min)
//   - Embeds the chat thread (system + user messages, text + image)
//   - Provides a composer for chat (text + Cloudinary-backed image upload)
//
// Realtime: polls every 5 seconds for trade + messages. WS replacement is
// a Phase-1.5 follow-up; polling keeps the wire shape simple.
import * as ImagePicker from 'expo-image-picker';
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Image,
  KeyboardAvoidingView,
  Modal,
  Platform,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useAuth } from '../../../AuthContext/AuthContext';
import { API_BASE_URL } from '../../../constants/api/config';
import type { ChainKey } from '../../../constants/chains';
import {
  formatTokenAmount,
  messagesApi,
  tradesApi,
  type P2PMessage,
  type P2PTrade,
  type UnsignedTx,
} from '../../../services/p2p';
import { broadcastTransaction, getActiveWallet, signTransaction } from '../../../services/wallet';
import { getAuthHeaders } from '../../../services/walletApi';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
  info: '#3B82F6',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };
const POLL_INTERVAL_MS = 5000;

const STAGES: { key: P2PTrade['status']; label: string }[] = [
  { key: 'pending_payment', label: 'Pending payment' },
  { key: 'paid', label: 'Paid' },
  { key: 'released', label: 'Released' },
];

const TradeDetailScreen: React.FC = () => {
  const { tradeId } = useLocalSearchParams<{ tradeId: string }>();
  const { currentUser } = useAuth();
  const user = currentUser as { id?: string } | null;
  const [trade, setTrade] = useState<P2PTrade | null>(null);
  const [messages, setMessages] = useState<P2PMessage[]>([]);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<string>(''); // current action stage
  const [showDispute, setShowDispute] = useState(false);
  const [disputeReason, setDisputeReason] = useState('');
  const [composerText, setComposerText] = useState('');
  const [composerImage, setComposerImage] = useState<string | null>(null);
  const scrollRef = useRef<ScrollView>(null);

  // Stable role detection — buyer or seller. Admin views go through the
  // admin disputes screen, not this one.
  const role: 'buyer' | 'seller' | null = useMemo(() => {
    if (!trade || !user) return null;
    if (trade.buyerId === user.id) return 'buyer';
    if (trade.sellerId === user.id) return 'seller';
    return null;
  }, [trade, user]);

  // Acting ref mirrors `acting` so the poll callback can read the
  // latest value without re-binding the interval each time.
  const actingRef = useRef('');

  const load = useCallback(async () => {
    if (!tradeId) return;
    const [tradeRes, msgRes] = await Promise.all([
      tradesApi.get(String(tradeId)),
      messagesApi.list(String(tradeId)),
    ]);
    if (tradeRes.success && tradeRes.data) setTrade(tradeRes.data);
    if (msgRes.success && msgRes.data) setMessages(msgRes.data);
    setLoading(false);
  }, [tradeId]);

  useEffect(() => {
    load();
    // Skip polls while a sign/broadcast/confirm flow is in flight, so an
    // intermediate server fetch doesn't clobber optimistic local state.
    const id = setInterval(() => {
      if (!actingRef.current) load();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, [load]);

  const startActing = useCallback((stage: string) => {
    actingRef.current = stage;
    setActing(stage);
  }, []);
  const stopActing = useCallback(() => {
    actingRef.current = '';
    setActing('');
  }, []);

  // Auto-scroll the chat to the latest message when count grows.
  const lastMessageCount = useRef(0);
  useEffect(() => {
    if (messages.length > lastMessageCount.current) {
      setTimeout(() => scrollRef.current?.scrollToEnd({ animated: true }), 80);
    }
    lastMessageCount.current = messages.length;
  }, [messages.length]);

  if (loading || !trade || !role) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }

  const tokenDecimals = TOKEN_DECIMALS[trade.token] ?? 18;
  const isTerminal =
    trade.status === 'released' ||
    trade.status === 'cancelled' ||
    trade.status === 'resolved';

  // ─── Actions ──────────────────────────────────────────────────────────────

  const handleMarkPaid = async () => {
    if (acting) return;
    // Guardrail: marking paid is irreversible and signals the seller to release.
    // Force buyer to explicitly confirm they actually transferred the fiat.
    const confirmed = await new Promise<boolean>((resolve) => {
      Alert.alert(
        'Confirm fiat payment',
        'Only tap "I have paid" after the bank transfer is sent. The seller will release your ' +
          trade.token +
          ' based on your confirmation, and this cannot be undone.',
        [
          { text: 'Not yet', style: 'cancel', onPress: () => resolve(false) },
          { text: 'I have paid', style: 'destructive', onPress: () => resolve(true) },
        ],
      );
    });
    if (!confirmed) return;

    // Buyer must upload payment proof before flipping state.
    const proofUrl = await pickAndUploadImage(startActing);
    if (!proofUrl) {
      stopActing();
      return;
    }
    startActing('Marking as paid…');
    const res = await tradesApi.markPaid(trade.id, proofUrl);
    if (res.success && res.data) {
      setTrade(res.data);
      load();
    } else {
      Alert.alert('Could not mark paid', res.message || 'Server rejected the action');
    }
    stopActing();
  };

  const handleRelease = async () => {
    if (acting) return;
    let broadcastHash: string | null = null;
    try {
      startActing('Loading wallet…');
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) throw new Error('No wallet found');

      startActing('Preparing release tx…');
      const prep = await tradesApi.prepareRelease(trade.id, sellerAddress);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');

      startActing('Signing…');
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));

      startActing('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      startActing('Confirming…');
      const conf = await tradesApi.confirmRelease(trade.id, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
      setTrade(conf.data ?? trade);
      load();
    } catch (err: any) {
      handlePostBroadcastError('Release', broadcastHash, err);
    } finally {
      stopActing();
    }
  };

  const handleCancel = async () => {
    if (acting) return;
    let broadcastHash: string | null = null;
    try {
      startActing('Loading wallet…');
      const callerAddress = await getActiveWallet();
      if (!callerAddress) throw new Error('No wallet found');

      startActing('Preparing cancel…');
      const prep = await tradesApi.prepareCancel(trade.id, callerAddress);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');

      startActing('Signing…');
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));

      startActing('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      startActing('Confirming…');
      const conf = await tradesApi.confirmCancel(trade.id, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
      setTrade(conf.data ?? trade);
      load();
    } catch (err: any) {
      handlePostBroadcastError('Cancel', broadcastHash, err);
    } finally {
      stopActing();
    }
  };

  const handleSubmitDispute = async () => {
    if (acting || !disputeReason.trim()) return;
    let broadcastHash: string | null = null;
    try {
      startActing('Loading wallet…');
      const callerAddress = await getActiveWallet();
      if (!callerAddress) throw new Error('No wallet found');

      startActing('Preparing dispute…');
      const prep = await tradesApi.raiseDispute(trade.id, disputeReason.trim(), callerAddress);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');

      startActing('Signing…');
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));

      startActing('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      startActing('Confirming…');
      const conf = await tradesApi.confirmRaiseDispute(trade.id, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
      setTrade(conf.data ?? trade);
      setShowDispute(false);
      setDisputeReason('');
      load();
    } catch (err: any) {
      handlePostBroadcastError('Dispute', broadcastHash, err);
    } finally {
      stopActing();
    }
  };

  const handleSendMessage = async () => {
    if (composerImage) {
      const url = await uploadImage(composerImage, startActing);
      stopActing();
      if (url) {
        const res = await messagesApi.sendImage(trade.id, url);
        if (res.success && res.data) {
          setMessages((prev) => [...prev, res.data!]);
        } else {
          Alert.alert('Send failed', res.message || 'Server rejected the message');
        }
      }
      setComposerImage(null);
      return;
    }
    const text = composerText.trim();
    if (!text) return;
    setComposerText('');
    const res = await messagesApi.sendText(trade.id, text);
    if (res.success && res.data) {
      setMessages((prev) => [...prev, res.data!]);
    } else {
      Alert.alert('Send failed', res.message || 'Server rejected the message');
      setComposerText(text); // restore so user doesn't lose their message
    }
  };

  const pickImage = async () => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== 'granted') {
      Alert.alert('Permission required', 'Allow photo library access to attach images.');
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      allowsEditing: false,
      quality: 0.8,
    });
    if (!result.canceled && result.assets?.[0]) {
      setComposerImage(result.assets[0].uri);
    }
  };

  // ─── Render ───────────────────────────────────────────────────────────────

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
      >
        <ScrollView
          ref={scrollRef}
          contentContainerStyle={styles.scroll}
          keyboardShouldPersistTaps="handled"
        >
          <StatusBar status={trade.status} />

          <View style={styles.summary}>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Amount</Text>
              <Text style={styles.summaryValue}>
                {formatTokenAmount(trade.tokenAmount, tokenDecimals, 4)} {trade.token}
              </Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Fiat</Text>
              <Text style={styles.summaryValue}>
                ₦{Number(trade.fiatAmount).toLocaleString()}
              </Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Rate</Text>
              <Text style={styles.summaryValue}>₦{trade.rate} / {trade.token}</Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Your role</Text>
              <Text style={[styles.summaryValue, { textTransform: 'capitalize' }]}>
                {role}
              </Text>
            </View>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Expires</Text>
              <Text style={styles.summaryValue}>{formatRelative(trade.expiresAt)}</Text>
            </View>
          </View>

          {!isTerminal && (
            <ActionPanel
              role={role}
              trade={trade}
              acting={acting}
              onMarkPaid={handleMarkPaid}
              onRelease={handleRelease}
              onCancel={handleCancel}
              onDispute={() => setShowDispute(true)}
            />
          )}

          <Text style={styles.chatHeading}>Chat</Text>
          <View style={styles.chatThread}>
            {messages.length === 0 ? (
              <Text style={styles.chatEmpty}>No messages yet.</Text>
            ) : (
              messages.map((m) => <MessageBubble key={m.id} message={m} isMine={m.senderId === user?.id} />)
            )}
          </View>
        </ScrollView>

        {!isTerminal && (
          <View style={styles.composer}>
            {composerImage ? (
              <View style={styles.composerImagePreview}>
                <Image source={{ uri: composerImage }} style={styles.composerImage} />
                <TouchableOpacity onPress={() => setComposerImage(null)} style={styles.composerImageRemove}>
                  <Icon name="close" size={18} color="#FFF" />
                </TouchableOpacity>
              </View>
            ) : (
              <TextInput
                style={styles.composerInput}
                value={composerText}
                onChangeText={setComposerText}
                placeholder="Type a message…"
                placeholderTextColor="#999"
                multiline
                editable={!acting}
              />
            )}
            <TouchableOpacity
              style={styles.composerButton}
              onPress={pickImage}
              disabled={!!acting}
            >
              <Icon name="image" size={22} color={GOLD.dark} />
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.composerButton, styles.composerSend]}
              onPress={handleSendMessage}
              disabled={!!acting || (!composerImage && !composerText.trim())}
            >
              <Icon name="send" size={20} color="#000" />
            </TouchableOpacity>
          </View>
        )}

        <DisputeModal
          visible={showDispute}
          token={trade.token}
          reason={disputeReason}
          onChange={setDisputeReason}
          onClose={() => setShowDispute(false)}
          onSubmit={handleSubmitDispute}
          busy={!!acting}
        />
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

// ─── Components ──────────────────────────────────────────────────────────────

const StatusBar: React.FC<{ status: P2PTrade['status'] }> = ({ status }) => {
  let stageIdx = STAGES.findIndex((s) => s.key === status);
  const isCancelled = status === 'cancelled';
  const isDisputed = status === 'disputed';
  const isResolved = status === 'resolved';
  const isReleasing = status === 'releasing';
  // Map non-linear states to the closest linear stage for visualization.
  if (isReleasing) stageIdx = 1; // visual marker between paid and released
  if (isResolved) stageIdx = 2;

  return (
    <View style={styles.statusBar}>
      <View style={styles.statusStages}>
        {STAGES.map((stage, idx) => {
          const reached = idx <= stageIdx && !isCancelled;
          return (
            <React.Fragment key={stage.key}>
              <View
                style={[
                  styles.statusDot,
                  reached && styles.statusDotActive,
                  isCancelled && idx === 0 && styles.statusDotCancelled,
                ]}
              >
                <Icon
                  name={
                    isCancelled
                      ? 'close'
                      : reached
                      ? 'check'
                      : 'radio-button-unchecked'
                  }
                  size={14}
                  color={reached || isCancelled ? '#000' : '#999'}
                />
              </View>
              {idx < STAGES.length - 1 && (
                <View
                  style={[
                    styles.statusLine,
                    idx < stageIdx && !isCancelled && styles.statusLineActive,
                  ]}
                />
              )}
            </React.Fragment>
          );
        })}
      </View>
      <Text style={styles.statusLabel}>
        {isCancelled
          ? 'Cancelled'
          : isDisputed
          ? 'Disputed — awaiting admin review'
          : isResolved
          ? 'Resolved by admin'
          : isReleasing
          ? 'Releasing on chain…'
          : STAGES[stageIdx]?.label || status}
      </Text>
    </View>
  );
};

const ActionPanel: React.FC<{
  role: 'buyer' | 'seller';
  trade: P2PTrade;
  acting: string;
  onMarkPaid: () => void;
  onRelease: () => void;
  onCancel: () => void;
  onDispute: () => void;
}> = ({ role, trade, acting, onMarkPaid, onRelease, onCancel, onDispute }) => {
  const expired = new Date(trade.expiresAt).getTime() < Date.now();
  const canMarkPaid = role === 'buyer' && trade.status === 'pending_payment';
  const canRelease =
    role === 'seller' && (trade.status === 'pending_payment' || trade.status === 'paid');
  const canCancel = expired && trade.status === 'pending_payment';
  const canDispute =
    trade.status === 'pending_payment' || trade.status === 'paid';
  const releasingDisabled = trade.status === 'releasing';

  return (
    <View style={styles.actions}>
      {acting ? (
        <View style={styles.actingRow}>
          <ActivityIndicator color={GOLD.dark} />
          <Text style={styles.actingText}>{acting}</Text>
        </View>
      ) : (
        <>
          {canMarkPaid && (
            <TouchableOpacity style={styles.primaryAction} onPress={onMarkPaid}>
              <Icon name="check-circle" size={20} color="#000" />
              <Text style={styles.primaryActionText}>I've paid — attach proof</Text>
            </TouchableOpacity>
          )}
          {canRelease && (
            <TouchableOpacity
              style={[styles.primaryAction, releasingDisabled && styles.actionDisabled]}
              onPress={onRelease}
              disabled={releasingDisabled}
            >
              <Icon name="lock-open" size={20} color="#000" />
              <Text style={styles.primaryActionText}>
                {releasingDisabled ? 'Releasing…' : `Release ${trade.token}`}
              </Text>
            </TouchableOpacity>
          )}
          <View style={styles.secondaryRow}>
            {canCancel && (
              <TouchableOpacity style={styles.secondaryAction} onPress={onCancel}>
                <Icon name="cancel" size={16} color="#666" />
                <Text style={styles.secondaryActionText}>Cancel (timed out)</Text>
              </TouchableOpacity>
            )}
            {canDispute && (
              <TouchableOpacity style={styles.secondaryAction} onPress={onDispute}>
                <Icon name="report" size={16} color={GOLD.error} />
                <Text style={[styles.secondaryActionText, { color: GOLD.error }]}>
                  Open dispute
                </Text>
              </TouchableOpacity>
            )}
          </View>
        </>
      )}
    </View>
  );
};

const MessageBubble: React.FC<{ message: P2PMessage; isMine: boolean }> = ({
  message,
  isMine,
}) => {
  if (message.type === 'system' || message.type === 'bot') {
    return (
      <View style={styles.sysMessage}>
        <Text style={styles.sysMessageText}>{message.body}</Text>
      </View>
    );
  }
  return (
    <View style={[styles.bubble, isMine ? styles.bubbleMine : styles.bubbleTheirs]}>
      {message.type === 'image' && message.imageUrl ? (
        <Image source={{ uri: message.imageUrl }} style={styles.bubbleImage} />
      ) : (
        <Text style={[styles.bubbleText, isMine && styles.bubbleTextMine]}>
          {message.body}
        </Text>
      )}
      <Text style={[styles.bubbleTime, isMine && { color: '#555' }]}>
        {formatTime(message.createdAt)}
      </Text>
    </View>
  );
};

const DisputeModal: React.FC<{
  visible: boolean;
  token: string;
  reason: string;
  onChange: (s: string) => void;
  onClose: () => void;
  onSubmit: () => void;
  busy: boolean;
}> = ({ visible, token, reason, onChange, onClose, onSubmit, busy }) => (
  <Modal animationType="slide" transparent visible={visible} onRequestClose={onClose}>
    <View style={styles.modalOverlay}>
      <View style={styles.modalCard}>
        <Text style={styles.modalTitle}>Open a dispute</Text>
        <Text style={styles.modalBody}>
          Explain what went wrong. Admins use this to decide whether the seller
          releases your {token} or you get a refund. Funds stay locked on chain
          until resolution.
        </Text>
        <TextInput
          style={styles.modalInput}
          multiline
          placeholder="e.g. I sent the fiat at 14:32 but seller hasn't released after 20 minutes."
          placeholderTextColor="#999"
          value={reason}
          onChangeText={onChange}
          maxLength={1000}
          editable={!busy}
        />
        <View style={styles.modalActions}>
          <TouchableOpacity style={styles.modalCancel} onPress={onClose} disabled={busy}>
            <Text style={styles.modalCancelText}>Cancel</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={[styles.modalSubmit, (!reason.trim() || busy) && styles.actionDisabled]}
            onPress={onSubmit}
            disabled={!reason.trim() || busy}
          >
            <Text style={styles.modalSubmitText}>{busy ? 'Working…' : 'Open dispute'}</Text>
          </TouchableOpacity>
        </View>
      </View>
    </View>
  </Modal>
);

// ─── Helpers ────────────────────────────────────────────────────────────────

// Distinguish failures that happen BEFORE the tx hits the chain (safe to retry)
// from failures that happen AFTER (tx may have settled — DO NOT just retry).
// Without this, a server-side confirm error looks identical to a prepare error
// and users double-spend by retrying.
function handlePostBroadcastError(
  action: string,
  broadcastHash: string | null,
  err: unknown,
): void {
  const detail = (err as { message?: string } | undefined)?.message || String(err);
  if (broadcastHash) {
    Alert.alert(
      `${action} submitted, server confirm failed`,
      `Tx ${broadcastHash} was broadcast on chain. The server could not record it ` +
        `(${detail}). Don't retry — wait a minute and the reconciler will pick it up. ` +
        `If status doesn't update, contact support with this tx hash.`,
    );
    return;
  }
  Alert.alert(`${action} failed`, detail);
}

async function uploadImage(
  uri: string,
  onStage: (s: string) => void
): Promise<string | null> {
  try {
    onStage('Uploading image…');
    const form = new FormData();
    // RN FormData accepts the file-descriptor object shape.
    form.append('image', {
      uri,
      name: `p2p-${Date.now()}.jpg`,
      type: 'image/jpeg',
    } as any);
    const headers = await getAuthHeaders();
    delete headers['Content-Type']; // let RN set the multipart boundary
    const res = await fetch(`${API_BASE_URL}/upload`, {
      method: 'POST',
      headers,
      body: form as any,
    });
    const json = await res.json();
    if (json?.success && json.data?.url) return json.data.url as string;
    Alert.alert('Upload failed', json?.message || 'Cloudinary rejected the upload');
    return null;
  } catch (err: any) {
    Alert.alert('Upload failed', err?.message || String(err));
    return null;
  }
}

async function pickAndUploadImage(onStage: (s: string) => void): Promise<string | null> {
  const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
  if (status !== 'granted') {
    Alert.alert('Permission required', 'Allow photo library access to attach proof.');
    return null;
  }
  const result = await ImagePicker.launchImageLibraryAsync({
    mediaTypes: ImagePicker.MediaTypeOptions.Images,
    quality: 0.8,
  });
  if (result.canceled || !result.assets?.[0]) return null;
  return uploadImage(result.assets[0].uri, onStage);
}

function toTxLike(tx: UnsignedTx) {
  const ensure0x = (v?: string) =>
    !v ? undefined : v.startsWith('0x') ? v : `0x${v}`;
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
  const ms = new Date(iso).getTime() - Date.now();
  if (Number.isNaN(ms)) return iso;
  if (ms < 0) return `${Math.abs(Math.round(ms / 60000))}m ago`;
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`;
  return `${Math.round(ms / 60_000)}m`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 16, paddingBottom: 16 },

  statusBar: { marginBottom: 16, alignItems: 'center' },
  statusStages: { flexDirection: 'row', alignItems: 'center', marginBottom: 8 },
  statusDot: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: '#EEE',
    alignItems: 'center',
    justifyContent: 'center',
  },
  statusDotActive: { backgroundColor: GOLD.primary },
  statusDotCancelled: { backgroundColor: GOLD.error },
  statusLine: { width: 32, height: 2, backgroundColor: '#EEE', marginHorizontal: 4 },
  statusLineActive: { backgroundColor: GOLD.primary },
  statusLabel: { fontSize: 13, color: '#444', fontWeight: '700' },

  summary: {
    backgroundColor: '#FFF',
    borderRadius: 12,
    padding: 14,
    borderWidth: 1,
    borderColor: GOLD.muted,
    marginBottom: 14,
  },
  summaryRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 4 },
  summaryLabel: { fontSize: 13, color: '#888' },
  summaryValue: { fontSize: 14, color: '#000', fontWeight: '700' },

  actions: { marginBottom: 18 },
  actingRow: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD.light,
    paddingHorizontal: 14,
    paddingVertical: 14,
    borderRadius: 10,
  },
  actingText: { marginLeft: 10, color: GOLD.dark, fontWeight: '700' },
  primaryAction: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD.primary,
    paddingVertical: 14,
    borderRadius: 10,
    marginBottom: 10,
    shadowColor: GOLD.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.25,
    shadowRadius: 6,
    elevation: 4,
  },
  primaryActionText: { fontWeight: '800', color: '#000', marginLeft: 8 },
  secondaryRow: { flexDirection: 'row', gap: 12 },
  secondaryAction: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 8,
    paddingHorizontal: 12,
    borderRadius: 8,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: '#DDD',
  },
  secondaryActionText: { marginLeft: 6, color: '#666', fontWeight: '600', fontSize: 12 },
  actionDisabled: { opacity: 0.5 },

  chatHeading: { fontSize: 16, fontWeight: '800', marginBottom: 10, color: '#000' },
  chatThread: { gap: 8 },
  chatEmpty: { color: '#888', fontStyle: 'italic', textAlign: 'center', paddingVertical: 20 },
  bubble: {
    maxWidth: '85%',
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 14,
    marginBottom: 4,
  },
  bubbleMine: {
    backgroundColor: GOLD.primary,
    alignSelf: 'flex-end',
    borderBottomRightRadius: 4,
  },
  bubbleTheirs: {
    backgroundColor: '#FFF',
    alignSelf: 'flex-start',
    borderWidth: 1,
    borderColor: '#EEE',
    borderBottomLeftRadius: 4,
  },
  bubbleText: { fontSize: 14, color: '#000', lineHeight: 19 },
  bubbleTextMine: { color: '#000' },
  bubbleTime: { fontSize: 10, color: '#888', marginTop: 4 },
  bubbleImage: { width: 200, height: 200, borderRadius: 8 },
  sysMessage: {
    alignSelf: 'center',
    backgroundColor: '#EEF2F4',
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 10,
    maxWidth: '90%',
    marginVertical: 4,
  },
  sysMessageText: { fontSize: 12, color: '#555', textAlign: 'center', fontStyle: 'italic' },

  composer: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    paddingHorizontal: 10,
    paddingVertical: 8,
    backgroundColor: '#FFF',
    borderTopWidth: 1,
    borderTopColor: '#EEE',
  },
  composerInput: {
    flex: 1,
    minHeight: 38,
    maxHeight: 100,
    backgroundColor: '#F5F5F5',
    borderRadius: 18,
    paddingHorizontal: 12,
    paddingVertical: 8,
    color: '#000',
    fontSize: 14,
  },
  composerImagePreview: { flex: 1, position: 'relative', marginRight: 8 },
  composerImage: { width: 80, height: 80, borderRadius: 8 },
  composerImageRemove: {
    position: 'absolute',
    top: 4,
    right: 4,
    backgroundColor: 'rgba(0,0,0,0.6)',
    borderRadius: 12,
    width: 24,
    height: 24,
    alignItems: 'center',
    justifyContent: 'center',
  },
  composerButton: {
    width: 38,
    height: 38,
    alignItems: 'center',
    justifyContent: 'center',
    marginLeft: 4,
  },
  composerSend: {
    backgroundColor: GOLD.primary,
    borderRadius: 19,
  },

  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'center',
    paddingHorizontal: 20,
  },
  modalCard: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 20,
  },
  modalTitle: { fontSize: 18, fontWeight: '800', color: '#000', marginBottom: 8 },
  modalBody: { fontSize: 13, color: '#666', marginBottom: 14, lineHeight: 18 },
  modalInput: {
    minHeight: 100,
    backgroundColor: '#F5F5F5',
    borderRadius: 10,
    padding: 12,
    color: '#000',
    textAlignVertical: 'top',
    fontSize: 14,
  },
  modalActions: {
    flexDirection: 'row',
    justifyContent: 'flex-end',
    marginTop: 16,
    gap: 10,
  },
  modalCancel: { paddingVertical: 10, paddingHorizontal: 16 },
  modalCancelText: { color: '#666', fontWeight: '600' },
  modalSubmit: {
    paddingVertical: 10,
    paddingHorizontal: 18,
    backgroundColor: GOLD.error,
    borderRadius: 10,
  },
  modalSubmitText: { color: '#FFF', fontWeight: '800' },
});

export default TradeDetailScreen;
