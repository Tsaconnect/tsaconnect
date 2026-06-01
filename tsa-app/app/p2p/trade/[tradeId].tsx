// app/p2p/trade/[tradeId].tsx
//
// The state-machine cockpit for a single P2P trade. Single screen that:
//
//   - Renders the trade's current status as a visual progress timeline
//   - Shows trade summary (counterparty, amount, rate, payment method)
//   - Surfaces context-aware actions per role + status:
//       buyer / pending_payment    → Mark as paid (with proof upload)
//       seller / pending or paid   → Release
//       either / paid              → Open dispute (navigates to the wizard)
//       either / past expiry       → Cancel (anyone after 30 min)
//   - Embeds the chat thread (system + user messages, text + image)
//   - Provides a composer for chat (text + Cloudinary-backed image upload)
//
// Realtime: event-driven. useTradeRealtime subscribes to the per-user WS
// (NotificationContext) and refreshes instantly on this trade's p2p.trade.*
// transitions and p2p.message.new frames. A slow (30s) focus-gated safety poll
// + a reconcile on (re)connect/focus backstop the best-effort socket (events
// can be dropped/missed, and system messages emit no event).
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// Chat, countdown, and all markPaid/release/cancel action handlers (incl.
// post-broadcast error handling) are preserved verbatim; only the refresh
// transport changed (5s poll → events + safety poll). The dispute button
// navigates to /p2p/dispute/<tradeId> (the old inline modal is gone).
import * as ImagePicker from 'expo-image-picker';
import { router, useLocalSearchParams } from 'expo-router';
import { useIsFocused } from '@react-navigation/native';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Image,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  TextInput,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Button,
  Card,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  StatusPill,
  Text,
} from '@/components/ui';
import { useAuth } from '../../../AuthContext/AuthContext';
import { API_BASE_URL } from '../../../constants/api/config';
import type { ChainKey } from '../../../constants/chains';
import { useCountdown } from '../../../hooks/useCountdown';
import { useTradeExpiryReminder } from '../../../hooks/useTradeExpiryReminder';
import { useTradeRealtime } from '../../../hooks/useTradeRealtime';
import { useNotifications } from '../../../contexts/NotificationContext';
import {
  formatTokenAmount,
  messagesApi,
  ratingsApi,
  tradesApi,
  type P2PMessage,
  type P2PTrade,
  type UnsignedTx,
} from '../../../services/p2p';
import { broadcastTransaction, getActiveWallet, signTransaction } from '../../../services/wallet';
import { getAuthHeaders } from '../../../services/walletApi';

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };
// Slow safety reconcile — the fast path is WS events (useTradeRealtime). This
// only backstops dropped/missed events and system messages (which emit none).
const SAFETY_POLL_INTERVAL_MS = 30000;

const STAGES: { key: P2PTrade['status']; label: string }[] = [
  { key: 'pending_payment', label: 'Pending payment' },
  { key: 'paid', label: 'Paid' },
  { key: 'released', label: 'Released' },
];

const TradeDetailScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { tradeId } = useLocalSearchParams<{ tradeId: string }>();
  const { currentUser } = useAuth();
  const user = currentUser as { id?: string } | null;
  const { connected } = useNotifications();
  const [trade, setTrade] = useState<P2PTrade | null>(null);
  const [messages, setMessages] = useState<P2PMessage[]>([]);
  const [loading, setLoading] = useState(true);
  const [acting, setActing] = useState<string>(''); // current action stage
  const [composerText, setComposerText] = useState('');
  const [composerImage, setComposerImage] = useState<string | null>(null);
  const [rated, setRated] = useState(false);
  const scrollRef = useRef<ScrollView>(null);

  const countdown = useCountdown(trade?.status === 'pending_payment' ? trade?.expiresAt : undefined);

  // Local notification ~5 min before a pending_payment trade expires.
  useTradeExpiryReminder(trade);

  const rate = async (value: 1 | -1) => {
    const res = await ratingsApi.rate(String(tradeId), value);
    if (res.success || res.status === 409) setRated(true);
  };

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

  // Mirror messages into a ref so the WS message refresher can read the latest
  // cursor without re-subscribing the socket on every new message.
  const messagesRef = useRef<P2PMessage[]>([]);
  useEffect(() => {
    messagesRef.current = messages;
  }, [messages]);

  // Fast-path refreshers fired by useTradeRealtime on WS frames for this trade.
  // onTrade does a full reconcile (also surfaces system chat rows, which emit no
  // event); both skip while an action is in flight so they can't clobber
  // optimistic markPaid/release/cancel state.
  const reloadTrade = useCallback(() => {
    if (actingRef.current) return;
    load();
  }, [load]);

  const reloadMessages = useCallback(async () => {
    if (!tradeId || actingRef.current) return;
    const list = messagesRef.current;
    const since = list.length ? list[list.length - 1].createdAt : undefined;
    const res = await messagesApi.list(String(tradeId), since ? { since } : {});
    if (res.success && res.data?.length) {
      setMessages((prev) => {
        const seen = new Set(prev.map((m) => m.id));
        const fresh = res.data!.filter((m) => !seen.has(m.id));
        return fresh.length ? [...prev, ...fresh] : prev;
      });
    }
  }, [tradeId]);

  // A terminal trade (released/cancelled/resolved) won't change again.
  const isTerminal =
    trade?.status === 'released' ||
    trade?.status === 'cancelled' ||
    trade?.status === 'resolved';

  // Refresh only while the screen is focused and the trade can still change.
  // An unfocused screen (pushed over, or a backgrounded tab) or a terminal trade
  // means any traffic is wasted — so both the WS subscription and the safety
  // poll are gated on this.
  const isFocused = useIsFocused();
  const pollingActive = isFocused && !isTerminal;

  // Fast path: refresh instantly on WS frames for THIS trade.
  useTradeRealtime(
    tradeId ? String(tradeId) : undefined,
    { onTrade: reloadTrade, onMessage: reloadMessages },
    pollingActive,
  );

  useEffect(() => {
    // Fetch once on (re)focus of a live trade, then run a slow safety reconcile.
    // We deliberately don't load on the way *out*: action handlers call load()
    // themselves, so a trade that just went terminal is already current.
    if (!pollingActive) return;
    load();
    // Skip a tick while a sign/broadcast/confirm flow is in flight, so an
    // intermediate server fetch doesn't clobber optimistic local state.
    const id = setInterval(() => {
      if (!actingRef.current) load();
    }, SAFETY_POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, [load, pollingActive]);

  // On socket (re)connect, do one full reconcile — events aren't replayed across
  // a disconnect, so anything missed while the WS was down is caught here.
  useEffect(() => {
    if (connected && pollingActive) load();
    // Intentionally only on the `connected` edge; focus/mount loads are handled
    // by the effect above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connected]);

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

  if (loading || !trade) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <Spinner label="Loading trade…" />
        </View>
      </SafeAreaView>
    );
  }
  if (!role) {
    // Loaded trade, but the viewer is neither buyer nor seller.
    // Admin lands on the admin queue, not here, so this is an error state.
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View
          style={{
            flex: 1,
            alignItems: 'center',
            justifyContent: 'center',
            paddingHorizontal: spacing['3xl'],
            gap: spacing.md,
          }}
        >
          <Icon name="lock" size={48} color={colors.textMuted} />
          <Text variant="title2" weight="700" align="center">
            Not your trade
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center">
            You&apos;re not a party to this trade. If you&apos;re an admin reviewing a
            dispute, open it from the admin queue.
          </Text>
          <Button title="Go back" variant="secondary" fullWidth={false} onPress={() => router.back()} />
        </View>
      </SafeAreaView>
    );
  }

  const tokenDecimals = TOKEN_DECIMALS[trade.token] ?? 18;

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
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx), sellerAddress);

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
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx), callerAddress);

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

  const canSend = !acting && (!!composerImage || !!composerText.trim());

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
      >
        <ScrollView
          ref={scrollRef}
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing.xl }}
          keyboardShouldPersistTaps="handled"
        >
          <StatusTimeline status={trade.status} />

          <Card style={{ marginTop: spacing.lg }}>
            <SummaryLine label="Amount">
              <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
                <AmountText
                  value={formatTokenAmount(trade.tokenAmount, tokenDecimals, 4)}
                  variant="bodyMed"
                />
                <Text variant="bodySm" color="textMuted">
                  {trade.token}
                </Text>
              </View>
            </SummaryLine>
            <SummaryLine label="Fiat">
              <AmountText value={trade.fiatAmount} currency={trade.fiatCurrency} variant="bodyMed" />
            </SummaryLine>
            <SummaryLine label="Rate">
              <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
                <AmountText value={trade.rate} currency={trade.fiatCurrency} variant="bodySm" />
                <Text variant="caption" color="textMuted">
                  / {trade.token}
                </Text>
              </View>
            </SummaryLine>
            <SummaryLine label="Your role">
              <Text variant="bodySm" weight="700" style={{ textTransform: 'capitalize' }}>
                {role}
              </Text>
            </SummaryLine>
            <SummaryLine label="Expires">
              {trade.status === 'pending_payment' ? (
                <Text
                  variant="bodyMed"
                  weight="700"
                  style={{ color: countdown.expired ? colors.danger : colors.escrow }}
                >
                  {countdown.expired ? 'Expired' : `${countdown.label} left to pay`}
                </Text>
              ) : (
                <Text variant="bodySm" weight="700" color="textSecondary">
                  {formatRelative(trade.expiresAt)}
                </Text>
              )}
            </SummaryLine>
          </Card>

          {!isTerminal && (
            <ActionPanel
              role={role}
              trade={trade}
              acting={acting}
              onMarkPaid={handleMarkPaid}
              onRelease={handleRelease}
              onCancel={handleCancel}
              onDispute={() => router.push(`/p2p/dispute/${trade.id}`)}
            />
          )}

          {trade.status === 'released' && !rated && (
            <Card style={{ marginTop: spacing.lg }}>
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                }}
              >
                <Text variant="bodyMed" weight="700">
                  Rate this trade
                </Text>
                <View style={{ flexDirection: 'row', gap: spacing['2xl'] }}>
                  <IconButton
                    icon="thumbUp"
                    accessibilityLabel="Rate positive"
                    color={colors.success}
                    onPress={() => rate(1)}
                  />
                  <IconButton
                    icon="thumbDown"
                    accessibilityLabel="Rate negative"
                    color={colors.danger}
                    onPress={() => rate(-1)}
                  />
                </View>
              </View>
            </Card>
          )}
          {trade.status === 'released' && rated && (
            <Text
              variant="bodySm"
              weight="600"
              color="accent"
              align="center"
              style={{ marginTop: spacing.lg }}
            >
              Thanks for your feedback.
            </Text>
          )}

          <Text variant="title3" weight="700" style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
            Chat
          </Text>
          <View style={{ gap: spacing.sm }}>
            {messages.length === 0 ? (
              <Text
                variant="bodySm"
                color="textMuted"
                align="center"
                style={{ paddingVertical: spacing.xl, fontStyle: 'italic' }}
              >
                No messages yet.
              </Text>
            ) : (
              messages.map((m) => (
                <MessageBubble key={m.id} message={m} isMine={m.senderId === user?.id} />
              ))
            )}
          </View>
        </ScrollView>

        {!isTerminal && (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-end',
              gap: spacing.sm,
              paddingHorizontal: spacing.md,
              paddingVertical: spacing.sm,
              backgroundColor: colors.surface,
              borderTopWidth: 1,
              borderTopColor: colors.hairline,
            }}
          >
            {composerImage ? (
              <View style={{ flex: 1, position: 'relative' }}>
                <Image
                  source={{ uri: composerImage }}
                  style={{ width: 80, height: 80, borderRadius: 8 }}
                />
                <PressableScale
                  onPress={() => setComposerImage(null)}
                  accessibilityLabel="Remove attached image"
                  style={{
                    position: 'absolute',
                    top: 4,
                    left: 60,
                    backgroundColor: colors.overlay,
                    borderRadius: 12,
                    width: 24,
                    height: 24,
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <Icon name="close" size={16} color={colors.textInverse} />
                </PressableScale>
              </View>
            ) : (
              <View
                style={{
                  flex: 1,
                  minHeight: 38,
                  maxHeight: 100,
                  justifyContent: 'center',
                  backgroundColor: colors.surfaceAlt,
                  borderRadius: 18,
                  paddingHorizontal: spacing.md,
                }}
              >
                <TextInput
                  value={composerText}
                  onChangeText={setComposerText}
                  placeholder="Type a message…"
                  placeholderTextColor={colors.textMuted}
                  multiline
                  editable={!acting}
                  style={{
                    color: colors.textPrimary,
                    fontSize: 14,
                    paddingVertical: spacing.sm,
                  }}
                />
              </View>
            )}
            <IconButton
              icon="image"
              accessibilityLabel="Attach image"
              color={colors.accent}
              disabled={!!acting}
              onPress={pickImage}
            />
            <IconButton
              icon="send"
              variant="surface"
              accessibilityLabel="Send message"
              color={canSend ? colors.onAccent : colors.textMuted}
              disabled={!canSend}
              onPress={handleSendMessage}
              style={{ backgroundColor: canSend ? colors.accent : colors.surfaceAlt }}
            />
          </View>
        )}
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

// ─── Components ──────────────────────────────────────────────────────────────

const StatusTimeline: React.FC<{ status: P2PTrade['status'] }> = ({ status }) => {
  const { colors, spacing } = useTheme();
  let stageIdx = STAGES.findIndex((s) => s.key === status);
  const isCancelled = status === 'cancelled';
  const isDisputed = status === 'disputed';
  const isResolved = status === 'resolved';
  const isReleasing = status === 'releasing';
  // Map non-linear states to the closest linear stage for visualization.
  if (isReleasing) stageIdx = 1; // visual marker between paid and released
  if (isResolved) stageIdx = 2;

  // Resolve the headline status to a StatusPill for a single consistent rendering.
  return (
    <View style={{ alignItems: 'center', gap: spacing.md }}>
      <View style={{ flexDirection: 'row', alignItems: 'center' }}>
        {STAGES.map((stage, idx) => {
          const reached = idx <= stageIdx && !isCancelled;
          const cancelledHead = isCancelled && idx === 0;
          const dotBg = cancelledHead
            ? colors.danger
            : reached
            ? colors.accent
            : colors.surfaceAlt;
          const glyph: React.ComponentProps<typeof Icon>['name'] = cancelledHead
            ? 'close'
            : reached
            ? 'check'
            : 'checkCircle';
          const glyphColor = cancelledHead || reached ? colors.onAccent : colors.textMuted;
          return (
            <React.Fragment key={stage.key}>
              <View
                style={{
                  width: 28,
                  height: 28,
                  borderRadius: 14,
                  backgroundColor: dotBg,
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <Icon name={glyph} size={16} color={glyphColor} />
              </View>
              {idx < STAGES.length - 1 && (
                <View
                  style={{
                    width: 32,
                    height: 2,
                    marginHorizontal: spacing.xs,
                    backgroundColor:
                      idx < stageIdx && !isCancelled ? colors.accent : colors.hairline,
                  }}
                />
              )}
            </React.Fragment>
          );
        })}
      </View>
      {isDisputed || isResolved || isCancelled ? (
        <StatusPill kind="trade" status={status} />
      ) : (
        <Text variant="bodySm" weight="700" color="textSecondary">
          {isReleasing ? 'Releasing on chain…' : STAGES[stageIdx]?.label || status}
        </Text>
      )}
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
  const { colors, spacing } = useTheme();
  const expired = new Date(trade.expiresAt).getTime() < Date.now();
  // Role-awareness for both sell-ad and buy-ad trades:
  //   sell-ad trade: user is buyer (buys crypto), merchant is seller
  //     → buyer marks paid, seller (merchant) releases
  //   buy-ad trade:  user is seller (sells crypto), merchant is buyer
  //     → buyer (merchant) marks paid after sending fiat, seller (user) releases
  // The role field already resolves correctly from buyerId/sellerId vs user.id,
  // so canMarkPaid and canRelease gate correctly in both cases with no changes.
  const canMarkPaid = role === 'buyer' && trade.status === 'pending_payment';
  const canRelease =
    role === 'seller' && (trade.status === 'pending_payment' || trade.status === 'paid');
  const canCancel = expired && trade.status === 'pending_payment';
  const canDispute =
    trade.status === 'pending_payment' || trade.status === 'paid';
  const releasingDisabled = trade.status === 'releasing';

  return (
    <View style={{ marginTop: spacing.lg }}>
      {acting ? (
        <AccentCard padding="md">
          <Spinner label={acting} inline />
        </AccentCard>
      ) : (
        <View style={{ gap: spacing.md }}>
          {canMarkPaid && (
            <Button
              title="I've paid — attach proof"
              leftIcon="checkCircle"
              onPress={onMarkPaid}
            />
          )}
          {canRelease && (
            <Button
              title={releasingDisabled ? 'Releasing…' : `Release ${trade.token}`}
              leftIcon="lock"
              loading={releasingDisabled}
              disabled={releasingDisabled}
              onPress={onRelease}
            />
          )}
          {(canCancel || canDispute) && (
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              {canCancel && (
                <Button
                  title="Cancel (timed out)"
                  leftIcon="close"
                  variant="secondary"
                  size="sm"
                  fullWidth={false}
                  onPress={onCancel}
                />
              )}
              {canDispute && (
                <Button
                  title="Open dispute"
                  leftIcon="dispute"
                  variant="secondary"
                  size="sm"
                  fullWidth={false}
                  onPress={onDispute}
                  style={{ borderColor: colors.danger }}
                />
              )}
            </View>
          )}
        </View>
      )}
    </View>
  );
};

const MessageBubble: React.FC<{ message: P2PMessage; isMine: boolean }> = ({
  message,
  isMine,
}) => {
  const { colors, spacing, radius } = useTheme();
  if (message.type === 'system' || message.type === 'bot') {
    return (
      <View
        style={{
          alignSelf: 'center',
          backgroundColor: colors.surfaceAlt,
          paddingHorizontal: spacing.md,
          paddingVertical: spacing.xs,
          borderRadius: radius.md,
          maxWidth: '90%',
          marginVertical: spacing.xs,
        }}
      >
        <Text variant="caption" color="textSecondary" align="center" style={{ fontStyle: 'italic' }}>
          {message.body}
        </Text>
      </View>
    );
  }
  return (
    <View
      style={{
        maxWidth: '85%',
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.sm,
        borderRadius: radius.lg,
        alignSelf: isMine ? 'flex-end' : 'flex-start',
        backgroundColor: isMine ? colors.accent : colors.surface,
        borderWidth: isMine ? 0 : 1,
        borderColor: colors.hairline,
        borderBottomRightRadius: isMine ? radius.sm : radius.lg,
        borderBottomLeftRadius: isMine ? radius.lg : radius.sm,
      }}
    >
      {message.type === 'image' && message.imageUrl ? (
        <Image
          source={{ uri: message.imageUrl }}
          style={{ width: 200, height: 200, borderRadius: radius.sm }}
        />
      ) : (
        <Text variant="bodySm" style={{ color: isMine ? colors.onAccent : colors.textPrimary }}>
          {message.body}
        </Text>
      )}
      <Text
        variant="caption"
        style={{ marginTop: spacing.xs, color: isMine ? colors.onAccent : colors.textMuted }}
      >
        {formatTime(message.createdAt)}
      </Text>
    </View>
  );
};

const SummaryLine: React.FC<{ label: string; children: React.ReactNode }> = ({
  label,
  children,
}) => {
  const { spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingVertical: spacing.xs,
      }}
    >
      <Text variant="bodySm" color="textSecondary">
        {label}
      </Text>
      {children}
    </View>
  );
};

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

export default TradeDetailScreen;
