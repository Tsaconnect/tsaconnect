// app/wallet/paycrest/buy.tsx
//
// Buy stablecoins with Naira through Paycrest's provider network.
//
// Non-custodial end to end: the user transfers Naira straight to a liquidity
// provider's bank account, and the stablecoin lands in their own connected
// wallet. TSA brokers and records; it never holds either side.
//
// Two things drive the design, both learned from live testing:
//
//   1. Liquidity is not fixed. Providers join and leave the book and price by
//      order size — the same pair moved 4x overnight, and one day a whole
//      token had no onramp provider at all. So the route list is built from a
//      quote for the amount actually typed, never a hardcoded list. A route
//      the user can see is one the order will succeed on.
//   2. An order that expires while the user is still transferring can strand
//      their money at the provider's bank with no automatic refund. Hence the
//      prominent countdown and the warning well before it runs out.
//
// Flow: amount -> live routes -> confirm -> provider account + countdown ->
// the user pays in their bank app -> the order screen takes over.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  CopyableField,
  Input,
  PickerSheet,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getNigerianBanks, type NgBank } from '@/services/ngBanks';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import {
  createBuy,
  getQuote,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
} from '@/services/paycrestApi';

/** Debounce on the amount field: every keystroke would otherwise fan a live
 *  quote out across every network we support. */
const QUOTE_DEBOUNCE_MS = 450;

/** Warn while there is still time to act, not once it is hopeless. A transfer
 *  confirmed in ~10s in testing, but the user still has to open their bank app
 *  and type. */
const EXPIRY_WARNING_SECONDS = 300;

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

/** NUBAN length. */
const NUBAN_LENGTH = 10;

/** The refund account is insurance against a rare failure, not part of the
 *  purchase — asking for it on every buy is friction for something most users
 *  will never use. Remembered after the first time and shown as one line. */
const REFUND_STORE_KEY = 'paycrest.refundAccount.v1';

const venueId = (v: PaycrestVenue) => `${v.token}:${v.chain}`;
const venueLabel = (v: PaycrestVenue) =>
  `${v.token} on ${CHAIN_LABELS[v.chain] ?? v.chain}`;

export default function PaycrestBuyScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  // The asset the user picked upstream. A PREFERENCE, not a filter: a token
  // with no provider today must not leave them with an empty screen, so we
  // fall back to whatever the book can actually fill.
  const { asset } = useLocalSearchParams<{ asset?: string }>();

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);

  // Refund destination. Paycrest requires one on every onramp, because it
  // cannot know who will pay until the transfer arrives — so this is the only
  // thing that decides where money goes back to if the order fails. Asked
  // once and kept, rather than per purchase.
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [refundBank, setRefundBank] = useState<NgBank | null>(null);
  const [refundOpen, setRefundOpen] = useState(false);
  const [refundAccount, setRefundAccount] = useState('');

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [creating, setCreating] = useState(false);

  // Guards a late quote response from overwriting a newer one — the user types
  // faster than the network answers, and a stale route list would let them
  // pick something that cannot fill their current amount.
  const quoteSeq = useRef(0);

  const [editingRefund, setEditingRefund] = useState(false);

  useEffect(() => {
    getNigerianBanks()
      .then(async (list) => {
        setBanks(list);
        try {
          const saved = await AsyncStorage.getItem(REFUND_STORE_KEY);
          if (!saved) {
            setEditingRefund(true); // first time: ask
            return;
          }
          const { code, account } = JSON.parse(saved) as { code: string; account: string };
          const found = list.find((b) => b.code === code);
          if (found && account) {
            setRefundBank(found);
            setRefundAccount(account);
          } else {
            setEditingRefund(true);
          }
        } catch {
          setEditingRefund(true);
        }
      })
      .catch(() => setEditingRefund(true));
  }, []);

  useEffect(() => {
    const raw = amount.trim();
    if (!raw || Number(raw) <= 0) {
      setQuote(null);
      setVenue(null);
      setQuoteError(null);
      return;
    }
    const seq = ++quoteSeq.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      try {
        const q = await getQuote('buy', raw);
        if (seq !== quoteSeq.current) return;
        setQuote(q);
        setQuoteError(null);
        // Default to the cheapest, but keep the user's own pick if it survived
        // the amount change.
        setVenue((prev) => {
          const kept = prev && q.venues.find((v) => venueId(v) === venueId(prev));
          const preferred = asset
            ? q.venues.find((v) => v.token.toUpperCase() === asset.toUpperCase())
            : undefined;
          return kept ?? preferred ?? q.best ?? null;
        });
      } catch (e) {
        if (seq !== quoteSeq.current) return;
        setQuote(null);
        setVenue(null);
        // The server's wording is written for users — "no provider can fill
        // that amount right now" is actionable in a way "error" is not.
        setQuoteError(
          e instanceof PaycrestError
            ? e.message
            : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === quoteSeq.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [amount, asset]);

  const { label: countdown, expired, secs } = useCountdown(order?.validUntil);
  const expiringSoon = !expired && secs > 0 && secs <= EXPIRY_WARNING_SECONDS;

  const onConfirm = useCallback(async () => {
    if (!venue || !address || !refundBank) return;
    setCreating(true);
    try {
      const created = await createBuy({
        amount: amount.trim(),
        chain: venue.chain,
        token: venue.token,
        walletAddress: address,
        refundBankCode: refundBank.code,
        refundAccount,
      });
      // Remember it only once an order actually succeeded with it.
      AsyncStorage.setItem(
        REFUND_STORE_KEY,
        JSON.stringify({ code: refundBank.code, account: refundAccount }),
      ).catch(() => {});
      setOrder(created);
    } catch (e) {
      Alert.alert(
        'Could not start your purchase',
        e instanceof PaycrestError ? e.message : 'Please try again.',
      );
    } finally {
      setCreating(false);
    }
  }, [venue, address, amount, refundBank, refundAccount]);

  const pickerItems = useMemo(
    () =>
      (quote?.venues ?? []).map((v) => ({
        id: venueId(v),
        label: venueLabel(v),
        sublabel: `${v.tokenAmount.toFixed(4)} ${v.token} · ${
          v.spreadPercent >= 0 ? '+' : ''
        }${v.spreadPercent.toFixed(2)}% vs market`,
      })),
    [quote],
  );

  // ── Payment instructions ────────────────────────────────────────────────
  if (order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <Card>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              Transfer exactly
            </Text>
            <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
              ₦{order.payAmount}
            </Text>
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Includes the service fee, so it is slightly more than the ₦{order.amount} you entered.
            </Text>
          </Card>

          <Card
            style={{
              borderWidth: 1,
              borderColor: expired
                ? colors.danger
                : expiringSoon
                  ? colors.warning
                  : colors.border,
            }}
          >
            {expired ? (
              <>
                <Text variant="bodyMed" weight="700" style={{ color: colors.danger }}>
                  This order has expired
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Do not send money to this account. If you already sent it, open the order and
                  tap Get help — we will trace it for you.
                </Text>
              </>
            ) : (
              <>
                <Text
                  variant="bodySm"
                  style={{ color: expiringSoon ? colors.warning : colors.textSecondary }}
                >
                  Time left to pay
                </Text>
                <Text
                  variant="title1"
                  weight="700"
                  style={{ color: expiringSoon ? colors.warning : colors.textPrimary }}
                >
                  {countdown}
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Send the transfer now. Paying after this runs out can leave your money with the
                  provider, and it will not arrive automatically.
                </Text>
              </>
            )}
          </Card>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Pay into
            </Text>
            {/* Copyable, because a mistyped account number on a live transfer
                is not something we can undo. */}
            <CopyableField label="Bank" value={order.payInstitution ?? ''} />
            <CopyableField label="Account number" value={order.payAccountNumber ?? ''} />
            <CopyableField label="Account name" value={order.payAccountName ?? ''} />
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              This account belongs to the liquidity provider filling your order, not to TSA. It is
              valid for this order only — never reuse it.
            </Text>
          </Card>

          <SummaryCard>
            <SummaryRow label="You receive" value={`${order.tokenAmount} ${order.token}`} />
            <SummaryRow label="Rate" value={`₦${order.rate} / ${order.token}`} />
            <SummaryRow label="Network" value={CHAIN_LABELS[order.chain] ?? order.chain} />
          </SummaryCard>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Delivered to your wallet
            </Text>
            {/* Full address, never truncated — the user is entitled to verify
                where their crypto is going. */}
            <CopyableField value={order.walletAddress} />
          </Card>

          <Button
            title="I've sent the money"
            onPress={() => router.replace(`/wallet/paycrest/order/${order.id}` as never)}
            disabled={expired}
            fullWidth
          />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount + route ──────────────────────────────────────────────────────
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <AmountInput
            label="You pay"
            value={amount}
            onChangeText={setAmount}
            symbol="₦"
            symbolPosition="prefix"
            autoFocus
          />

          {/* The payoff sits directly under the input, because "what do I get"
              is the question the amount field is asking. Everything else on
              this screen is secondary to it. */}
          {(quoting || venue || quoteError) && (
            <Card>
              {quoting ? (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Spinner size="small" />
                  <Text variant="bodySm" style={{ color: colors.textMuted }}>
                    Finding the best rate…
                  </Text>
                </View>
              ) : quoteError ? (
                <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                  {quoteError}
                </Text>
              ) : venue ? (
                <>
                  <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                    You receive
                  </Text>
                  <Text variant="title1" weight="700" style={{ color: colors.textPrimary }}>
                    {venue.tokenAmount.toFixed(6)} {venue.token}
                  </Text>
                  <Text variant="caption" style={{ color: colors.textMuted }}>
                    on {CHAIN_LABELS[venue.chain] ?? venue.chain} · ₦{venue.rate} per {venue.token} ·{' '}
                    {venue.spreadPercent >= 0 ? '+' : ''}
                    {venue.spreadPercent.toFixed(2)}% vs market
                  </Text>
                  {/* Only offer a choice when there is one. A picker with a
                      single option is noise. */}
                  {quote && quote.venues.length > 1 && (
                    <Button
                      title={`Change route (${quote.venues.length} available)`}
                      variant="ghost"
                      size="sm"
                      onPress={() => setPickerOpen(true)}
                    />
                  )}
                </>
              ) : null}
            </Card>
          )}

          {/* Demoted: insurance, not part of the purchase. One line once known. */}
          {!editingRefund && refundBank ? (
            <SelectRow
              label="Refund account"
              value={`${refundBank.name} · ${refundAccount}`}
              onPress={() => setEditingRefund(true)}
            />
          ) : (
            <>
              <SelectRow
                label="Refund account"
                value={refundBank?.name}
                placeholder="Where should we refund you?"
                onPress={() => setRefundOpen(true)}
              />
              <Input
                label="Account number"
                value={refundAccount}
                onChangeText={(v: string) =>
                  setRefundAccount(v.replace(/\D/g, '').slice(0, NUBAN_LENGTH))
                }
                keyboardType="number-pad"
                placeholder="0123456789"
              />
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Used only if your order fails. Pay from this account so a refund reaches you.
              </Text>
            </>
          )}

          <Button
            title="Continue"
            onPress={onConfirm}
            loading={creating}
            disabled={
              !venue ||
              !address ||
              !refundBank ||
              refundAccount.length !== NUBAN_LENGTH ||
              quoting ||
              creating
            }
            fullWidth
          />
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — your crypto is delivered straight to it.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={pickerOpen}
        onClose={() => setPickerOpen(false)}
        title="Choose a network"
        items={pickerItems}
        selectedId={venue ? venueId(venue) : undefined}
        onSelect={(item) => {
          const picked = quote?.venues.find((v) => venueId(v) === item.id);
          if (picked) setVenue(picked);
          setPickerOpen(false);
        }}
        emptyText="No routes can fill this amount right now."
      />
      <PickerSheet
        visible={refundOpen}
        onClose={() => setRefundOpen(false)}
        title="Refund account"
        items={banks.map((b) => ({ id: b.code, label: b.name }))}
        selectedId={refundBank?.code}
        searchable
        onSelect={(item) => {
          setRefundBank(banks.find((b) => b.code === item.id) ?? null);
          setRefundOpen(false);
        }}
        emptyText="No banks available."
      />
    </SafeAreaView>
  );
}
