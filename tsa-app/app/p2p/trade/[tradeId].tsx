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
// markPaid, release and cancel are all on-chain txs that go through
// runTradeTx → services/p2pTx.runP2PTx (sponsored gas, prepare, sign,
// broadcast, wait mined, confirm). Refresh is events + a slow safety poll. The
// dispute button navigates to /p2p/dispute/<tradeId>.
import * as ImagePicker from 'expo-image-picker';
import { router, useLocalSearchParams } from 'expo-router';
import { useIsFocused } from '@react-navigation/native';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Image,
  KeyboardAvoidingView,
  Linking,
  Platform,
  RefreshControl,
  ScrollView,
  TextInput,
  View,
} from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
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
  useToast,
} from '@/components/ui';
import { PayToCard } from '@/components/p2p/PayToCard';
import { PaymentProof } from '@/components/p2p/PaymentProof';
import { payToFromPayee, payToFromPm } from '@/utils/payInto';
import { useAuth } from '../../../AuthContext/AuthContext';
import { API_BASE_URL } from '../../../constants/api/config';
import { MAINNET_CHAINS, TESTNET_CHAINS } from '../../../constants/chains';
import { useCountdown } from '../../../hooks/useCountdown';
import { useTradeExpiryReminder } from '../../../hooks/useTradeExpiryReminder';
import { useTradeRealtime } from '../../../hooks/useTradeRealtime';
import { usePendingTradeTx } from '../../../hooks/usePendingTradeTx';
import { useNotifications } from '../../../contexts/NotificationContext';
import {
  formatTokenAmount,
  messagesApi,
  p2pTokenDecimals,
  ratingsApi,
  tradesApi,
  type ApiResponse,
  type P2PDispute,
  type P2PMessage,
  type P2PTrade,
  type UnsignedTx,
} from '../../../services/p2p';
import { retryStashedConfirm, runP2PTx, txOutcomeAlert, type RunP2PTxResult } from '../../../services/p2pTx';
import { p2pErrorMessage, p2pKycCode } from '../../../services/p2pErrors';
import { getActiveWallet } from '../../../services/wallet';
import { promptKycRefusal } from '../../../utils/kycRefusal';
import { confirmAsync } from '../../../utils/confirmAsync';
import {
  markPaidConfirmMessage,
  OPEN_REVERTED_ALERT,
  pendingTradeTxCopy,
  RELEASE_CONFIRM_TITLE,
  releaseConfirmMessage,
  tradeActions,
  tradeFeeLines,
  tradeTxFollowUp,
  type TradeActions,
} from '../../../utils/p2pTradeView';
import { getAuthHeaders } from '../../../services/walletApi';

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
  const insets = useSafeAreaInsets();
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
  // Dispute for this trade — fetched lazily on a resolved trade so we can show
  // the on-chain resolution tx (resolveTxHash lives on the dispute, not trade).
  const [dispute, setDispute] = useState<P2PDispute | null>(null);
  // Proof uploaded per trade this session, so a retried mark-paid doesn't
  // re-pick or re-upload it.
  const proofUrlsRef = useRef<Record<string, string>>({});
  const toast = useToast();
  const [refreshing, setRefreshing] = useState(false);
  const scrollRef = useRef<ScrollView>(null);
  const inputRef = useRef<TextInput>(null);
  // Chat is collapsed by default — it's secondary to the trade actions. The
  // header shows a count of counterparty messages that arrived while it was
  // closed (the docked composer only mounts while the chat is open).
  const [chatOpen, setChatOpen] = useState(false);
  const [unread, setUnread] = useState(0);
  const seenCountRef = useRef(0);
  const chatBaselineSetRef = useRef(false);

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

  // Auto-scroll the chat to the latest message when count grows — only while the
  // chat is expanded (collapsed, the thread isn't rendered).
  const lastMessageCount = useRef(0);
  useEffect(() => {
    if (chatOpen && messages.length > lastMessageCount.current) {
      setTimeout(() => scrollRef.current?.scrollToEnd({ animated: true }), 80);
    }
    lastMessageCount.current = messages.length;
  }, [messages.length, chatOpen]);

  // Reveal the latest message + composer when the chat is opened.
  useEffect(() => {
    if (chatOpen) setTimeout(() => scrollRef.current?.scrollToEnd({ animated: true }), 120);
  }, [chatOpen]);

  // Track unread counterparty messages while the chat is collapsed. The baseline
  // is set once the initial history loads, so pre-existing messages don't count.
  useEffect(() => {
    if (loading) return;
    if (!chatBaselineSetRef.current) {
      chatBaselineSetRef.current = true;
      seenCountRef.current = messages.length;
      return;
    }
    if (chatOpen) {
      seenCountRef.current = messages.length;
      setUnread(0); // React no-ops if already 0
      return;
    }
    if (messages.length > seenCountRef.current) {
      // Count only real chat messages from the counterparty. System/bot rows
      // carry no senderId (so `senderId !== user.id` is trivially true) and are
      // emitted on every lifecycle transition — counting them would inflate the
      // badge past the human-visible chat. seenCountRef still advances to
      // messages.length below, so those rows are consumed, not re-counted later.
      const fresh = messages
        .slice(seenCountRef.current)
        .filter((m) => (m.type === 'text' || m.type === 'image') && m.senderId !== user?.id);
      seenCountRef.current = messages.length;
      if (fresh.length) setUnread((n) => n + fresh.length);
    }
  }, [loading, messages, chatOpen, user?.id]);

  // On a resolved trade, fetch the dispute once to surface its on-chain
  // resolution tx (both parties can then verify the admin settlement).
  useEffect(() => {
    if (trade?.status !== 'resolved' || !tradeId) return;
    let cancelled = false;
    tradesApi.getDispute(String(tradeId)).then((res) => {
      if (!cancelled && res.success && res.data) setDispute(res.data);
    });
    return () => {
      cancelled = true;
    };
  }, [trade?.status, tradeId]);

  // A trade tx this device sent whose confirm was lost (runP2PTx `stash`):
  // shown with a retry instead of re-offering the action until it goes stale.
  const { pending: pendingTx, stale: pendingStale, refresh: refreshPending } = usePendingTradeTx(trade);

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

  const tokenDecimals = p2pTokenDecimals(trade.chainId, trade.token);
  const feeLines = tradeFeeLines(trade, role, tokenDecimals);
  // A stale stash keeps its card and retry but no longer hides the buttons.
  const view = tradeActions(trade, role, Date.now(), pendingStale ? null : pendingTx?.mode);
  const pendingCopy = pendingTx ? pendingTradeTxCopy(pendingTx, pendingStale) : null;

  // ─── Actions ──────────────────────────────────────────────────────────────

  // Holds the action slot from the tap — before any dialog or await — so a fast
  // double tap can't start a second flow (React state would still read the old
  // value). Every user action on this trade goes through it. The stash is
  // re-read afterwards whether or not the action's reload got through.
  const withActing = async (fn: () => Promise<void>) => {
    if (actingRef.current) return;
    actingRef.current = 'Starting…';
    try {
      await fn();
    } finally {
      stopActing();
      refreshPending();
    }
  };

  const onPullRefresh = async () => {
    setRefreshing(true);
    try {
      await load();
    } finally {
      setRefreshing(false);
      refreshPending();
    }
  };

  // One path for every on-chain trade action (gas → prepare → sign →
  // broadcast → wait mined → confirm). Once broadcast, only a reverted receipt
  // reads as "failed" — anything else is "sent, still confirming", and the
  // stashed hash puts the confirming card (with a retry) in place of the action.
  const runTradeTx = async (
    label: string,
    action: 'mark_paid' | 'release' | 'cancel_trade',
    prepare: (wallet: string) => Promise<ApiResponse<{ unsignedTx: UnsignedTx | null }>>,
    confirm: (txHash: string) => Promise<ApiResponse<P2PTrade>>,
  ): Promise<RunP2PTxResult<unknown, P2PTrade>['status'] | undefined> => {
    try {
      startActing('Loading wallet…');
      const wallet = await getActiveWallet();
      if (!wallet) throw new Error('No wallet found. Set one up from the wallet screen first.');
      const r = await runP2PTx({
        chainId: trade.chainId,
        walletAddress: wallet,
        action,
        tradeId: trade.id,
        onStage: startActing,
        prepare: () => prepare(wallet),
        confirm: (txHash) => confirm(txHash),
        stash: () => ({ mode: action, tradeId: trade.id, token: trade.token, amount: trade.tokenAmount }),
      });
      if (r.status === 'refused' && promptKycRefusal(p2pKycCode(r.response), r.response.message, 'p2p')) return r.status;
      const next = tradeTxFollowUp(r);
      if (next.trade) setTrade(next.trade);
      const alert = txOutcomeAlert(label, r);
      if (alert) Alert.alert(alert.title, alert.message);
      if (next.reload) load();
      return r.status;
    } catch (err: any) {
      // Wallet lookup, signing or broadcast: nothing reached the chain.
      Alert.alert(`${label} failed`, p2pErrorMessage(err?.message));
      return undefined;
    }
  };

  const handleMarkPaid = () =>
    withActing(async () => {
      // Guardrail: marking paid is irreversible and signals the seller to release.
      const windowEnded = new Date(trade.expiresAt).getTime() < Date.now();
      const confirmed = await confirmAsync(
        'Confirm fiat payment',
        markPaidConfirmMessage(trade.token, windowEnded),
        'I have paid',
        'Not yet',
        true,
      );
      if (!confirmed) return;

      // The payer must upload payment proof before flipping state.
      let proofUrl: string | null = proofUrlsRef.current[trade.id] ?? null;
      if (!proofUrl) {
        proofUrl = await pickAndUploadImage(startActing);
        if (!proofUrl) return;
        proofUrlsRef.current[trade.id] = proofUrl;
      }
      const url = proofUrl;
      // R1: markPaid(tradeId) is signed by the fiat payer (the chain buyer) so
      // the seller can no longer cancelByTimeout. The signing wallet goes as
      // callerAddress: on a buy-ad the payer's chain buyer is the ad's seller
      // address, not their registered wallet. The DB flips to paid only after
      // the server sees the receipt and the TradePaid log.
      const status = await runTradeTx(
        'Mark paid',
        'mark_paid',
        (w) => tradesApi.prepareMarkPaid(trade.id, url, w),
        (h) => tradesApi.confirmMarkPaid(trade.id, h),
      );
      if (status === 'confirmed') delete proofUrlsRef.current[trade.id];
    });

  const handleRelease = () =>
    withActing(async () => {
      const ok = await confirmAsync(
        RELEASE_CONFIRM_TITLE,
        releaseConfirmMessage(trade, tokenDecimals),
        'Yes, release',
        'Not yet',
        true,
      );
      if (!ok) return;
      await runTradeTx(
        'Release',
        'release',
        (w) => tradesApi.prepareRelease(trade.id, w),
        (h) => tradesApi.confirmRelease(trade.id, h),
      );
    });

  const handleCancel = () =>
    withActing(async () => {
      await runTradeTx(
        'Cancel',
        'cancel_trade',
        (w) => tradesApi.prepareCancel(trade.id, w),
        (h) => tradesApi.confirmCancel(trade.id, h),
      );
    });

  // Re-send the lost confirm with the stashed hash — nothing is signed again.
  const handleRetryConfirm = () =>
    withActing(async () => {
      const rec = pendingTx;
      if (!rec) return;
      const { label } = pendingTradeTxCopy(rec);
      startActing('Checking the network…');
      const r = await retryStashedConfirm(rec);
      if (r.status === 'confirmed') {
        if (r.data) setTrade(r.data);
        toast.success(`${label} confirmed`);
      } else if (r.status === 'reverted') {
        const a = rec.mode === 'open'
          ? OPEN_REVERTED_ALERT
          : txOutcomeAlert(label, { status: 'reverted', txHash: rec.txHash, prepared: null });
        if (a) Alert.alert(a.title, a.message);
      } else {
        Alert.alert('Still confirming', r.message);
      }
      load();
    });

  const handleSendMessage = async () => {
    if (composerImage) {
      const url = await uploadImage(composerImage, startActing);
      stopActing();
      if (url) {
        const res = await messagesApi.sendImage(trade.id, url);
        if (res.success && res.data) {
          setMessages((prev) => [...prev, res.data!]);
        } else {
          Alert.alert('Send failed', p2pErrorMessage(res));
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
      Alert.alert('Send failed', p2pErrorMessage(res));
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
          refreshControl={
            <RefreshControl refreshing={refreshing} tintColor={colors.accent} onRefresh={onPullRefresh} />
          }
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
            {feeLines && (
              <>
                <SummaryLine label={feeLines.feeLabel}>
                  <Text variant="bodySm" numeric>{feeLines.feeValue}</Text>
                </SummaryLine>
                <SummaryLine label={feeLines.receiveLabel}>
                  <Text variant="bodySm" weight="700" numeric>{feeLines.receiveValue}</Text>
                </SummaryLine>
              </>
            )}
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
            {/* On-chain receipts — both buyer and seller can independently
                verify funding + settlement on the block explorer. */}
            {!!trade.openTxHash && isTxHash(trade.openTxHash) && (
              <SummaryLine label="Escrow tx">
                <SettlementTxLink chainId={trade.chainId} txHash={trade.openTxHash} />
              </SummaryLine>
            )}
            {trade.status === 'released' && !!trade.releaseTxHash && (
              <SummaryLine label="Release tx">
                <SettlementTxLink chainId={trade.chainId} txHash={trade.releaseTxHash} />
              </SummaryLine>
            )}
            {trade.status === 'resolved' && !!dispute?.resolveTxHash && (
              <SummaryLine label="Resolution tx">
                <SettlementTxLink chainId={trade.chainId} txHash={dispute.resolveTxHash} />
              </SummaryLine>
            )}
          </Card>

          {view.openUnconfirmed && (
            <AccentCard railColor={colors.warning} padding="md" style={{ marginTop: spacing.lg, gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700">
                Escrow still confirming
              </Text>
              <Text variant="bodySm" color="textSecondary">
                {pendingTx?.mode === 'open' && pendingCopy
                  ? pendingCopy.body
                  : 'Waiting for the network to confirm the escrow. This usually takes under a minute and updates on its own.'}
              </Text>
              {pendingTx?.mode === 'open' && (
                <Button
                  title="Retry confirm"
                  size="sm"
                  fullWidth={false}
                  loading={!!acting}
                  disabled={!!acting}
                  onPress={handleRetryConfirm}
                />
              )}
            </AccentCard>
          )}

          {!isTerminal && (
            <ActionPanel
              role={role}
              trade={trade}
              view={view}
              acting={acting}
              confirming={pendingTx && pendingTx.mode !== 'open' ? pendingCopy : null}
              onRetryConfirm={handleRetryConfirm}
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

          <PressableScale
            onPress={() => setChatOpen((o) => !o)}
            accessibilityLabel={chatOpen ? 'Collapse chat' : 'Expand chat'}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              marginTop: spacing.xl,
              marginBottom: chatOpen ? spacing.md : 0,
              paddingVertical: spacing.xs,
            }}
          >
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon
                name={chatOpen ? 'chevronDown' : 'chevronRight'}
                size={20}
                color={colors.textSecondary}
              />
              <Text variant="title3" weight="700">
                Chat
              </Text>
              {!chatOpen && unread > 0 && (
                <View
                  style={{
                    minWidth: 20,
                    height: 20,
                    borderRadius: 10,
                    paddingHorizontal: 6,
                    backgroundColor: colors.accent,
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  <Text variant="caption" weight="700" style={{ color: colors.onAccent }}>
                    {unread}
                  </Text>
                </View>
              )}
            </View>
            {!chatOpen && (
              <Text variant="caption" color="textMuted">
                {messages.length === 0
                  ? 'No messages'
                  : `${messages.length} message${messages.length === 1 ? '' : 's'}`}
              </Text>
            )}
          </PressableScale>
          {chatOpen && (
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
          )}
        </ScrollView>

        {!isTerminal && chatOpen && (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-end',
              gap: spacing.sm,
              paddingHorizontal: spacing.md,
              paddingTop: spacing.sm,
              // Add the bottom safe-area inset so the input clears the phone's
              // gesture bar / nav buttons (the screen's SafeAreaView only insets
              // the top, since the composer is docked outside the scroll area).
              paddingBottom: spacing.sm + insets.bottom,
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
                  maxHeight: 120,
                  justifyContent: 'center',
                  backgroundColor: colors.surfaceAlt,
                  borderRadius: 20,
                  paddingHorizontal: spacing.md,
                }}
              >
                {/* minHeight on the TextInput (not the wrapper) makes the WHOLE
                    pill a tap target — previously a centered, content-height
                    input left only a thin tappable strip in the middle. */}
                <TextInput
                  ref={inputRef}
                  value={composerText}
                  onChangeText={setComposerText}
                  placeholder="Type a message…"
                  placeholderTextColor={colors.textMuted}
                  multiline
                  editable={!acting}
                  style={{
                    color: colors.textPrimary,
                    fontSize: 14,
                    minHeight: 40,
                    paddingVertical: spacing.sm,
                    textAlignVertical: 'center',
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
  /** tradeActions for this trade and viewer. PM trades are automated (the
   *  backend drives markPaid + release); a dormant PM falls back to manual. */
  view: TradeActions;
  acting: string;
  /** A sent action still confirming: shown in place of the action buttons
   *  (above them once stale — view.confirmingTx decides). */
  confirming: { title: string; body: string } | null;
  onRetryConfirm: () => void;
  onMarkPaid: () => void;
  onRelease: () => void;
  onCancel: () => void;
  onDispute: () => void;
}> = ({ role, trade, view: a, acting, confirming, onRetryConfirm, onMarkPaid, onRelease, onCancel, onDispute }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ marginTop: spacing.lg }}>
      {acting ? (
        <AccentCard padding="md">
          <Spinner label={acting} inline />
        </AccentCard>
      ) : (
        <View style={{ gap: spacing.md }}>
          {a.showPmPayTo && trade.pmPayTo && (
            <PayToCard
              mode="pm"
              details={payToFromPm(trade.pmPayTo)}
              amount={trade.fiatAmount}
              currency={trade.fiatCurrency}
              payerName={trade.payerName}
            />
          )}
          {a.payToFor && trade.payee && (
            <PayToCard
              mode={a.payToFor}
              details={payToFromPayee(trade.payee)}
              amount={trade.fiatAmount}
              currency={trade.fiatCurrency}
            />
          )}
          {a.showProof && trade.proofImageUrl && (
            <PaymentProof url={trade.proofImageUrl} mine={role === 'buyer'} />
          )}
          {a.showPmAutomated && (
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                <Icon name="checkCircle" size={18} color={colors.accent} />
                <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                  We&apos;ll pay your bank and release once confirmed — no action
                  needed.
                </Text>
              </View>
            </AccentCard>
          )}
          {a.pmDormant && (
            <Text variant="caption" color="textMuted" align="center">
              Automated settlement unavailable — proceed manually.
            </Text>
          )}
          {confirming && (
            <AccentCard railColor={colors.warning} padding="md" style={{ gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700">
                {confirming.title}
              </Text>
              <Text variant="bodySm" color="textSecondary">
                {confirming.body}
              </Text>
              <Button title="Retry confirm" size="sm" fullWidth={false} onPress={onRetryConfirm} />
            </AccentCard>
          )}
          {a.sellerAwaitingPayment && (
            <Text variant="bodySm" color="textSecondary" align="center">
              Waiting for the buyer to send payment and mark it paid. You can
              release once they do.
            </Text>
          )}
          {a.canMarkPaid && (
            <Button
              title="I've paid — attach proof"
              leftIcon="checkCircle"
              onPress={onMarkPaid}
            />
          )}
          {a.canRelease && (
            <Button
              title={a.releasingDisabled ? 'Releasing…' : `Release ${trade.token}`}
              leftIcon="lock"
              loading={a.releasingDisabled}
              disabled={a.releasingDisabled}
              onPress={onRelease}
            />
          )}
          {(a.canCancel || a.canDispute) && (
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              {a.canCancel && (
                <Button
                  title="Cancel (timed out)"
                  leftIcon="close"
                  variant="secondary"
                  size="sm"
                  fullWidth={false}
                  onPress={onCancel}
                />
              )}
              {a.canDispute && (
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

// On-chain settlement receipt: truncated tx hash that opens the block explorer
// on tap, plus a copy affordance. Shown to BOTH parties on a released trade so
// either can independently verify the released tokens on-chain.
const SettlementTxLink: React.FC<{ chainId: number; txHash: string }> = ({ chainId, txHash }) => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  // Reconciler-repaired release: no real tx hash to show. Be honest rather than
  // render a broken link / copy a useless "reconciled" string.
  if (!isTxHash(txHash)) {
    return (
      <Text variant="bodySm" weight="600" color="textSecondary">
        Settled (reconciled)
      </Text>
    );
  }
  const url = explorerTxUrl(chainId, txHash);
  const short = `${txHash.slice(0, 6)}…${txHash.slice(-4)}`;
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
      <PressableScale
        onPress={() => {
          if (url) Linking.openURL(url).catch(() => toast.error('Could not open the explorer'));
        }}
        disabled={!url}
        accessibilityRole="link"
        accessibilityLabel="View release transaction on the block explorer"
      >
        <Text variant="bodySm" weight="600" color={url ? 'accent' : 'textSecondary'} numeric>
          {short}
        </Text>
      </PressableScale>
      <PressableScale
        onPress={async () => {
          await Clipboard.setStringAsync(txHash);
          toast.success('Transaction hash copied');
        }}
        accessibilityRole="button"
        accessibilityLabel="Copy transaction hash"
        hitSlop={8}
      >
        <Icon name="copy" size={14} color={colors.textMuted} />
      </PressableScale>
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

// A real 32-byte tx hash. The chain reconciler repairs a dropped confirm with a
// "reconciled" marker (not a hash), so guard against building a broken link.
function isTxHash(s: string): boolean {
  return /^0x[0-9a-fA-F]{64}$/.test(s);
}

// Build a block-explorer tx URL from the trade's chainId. Scans both mainnet
// and testnet registries (the trade carries an explicit chainId, which may
// differ from the app's currently-active network). Returns null if unknown.
function explorerTxUrl(chainId: number, txHash: string): string | null {
  if (!isTxHash(txHash)) return null;
  const chain = [
    ...Object.values(MAINNET_CHAINS),
    ...Object.values(TESTNET_CHAINS),
  ].find((c) => c.chainId === chainId);
  return chain ? `${chain.explorerUrl}/tx/${txHash}` : null;
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
