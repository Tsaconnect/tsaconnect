// app/wallet/paycrest/sell.tsx
//
// Sell stablecoins for Naira through Paycrest's provider network.
//
// Non-custodial: the user sends the stablecoin FROM their own connected wallet
// to the provider's address, and the provider pays their bank directly. TSA
// never holds the asset or the Naira — which also means we cannot reverse a
// send, so the deposit address and amount are shown copyably and never
// truncated.
//
// The user types the Naira they want to receive; the token amount is derived
// from the live quote. That is the right way round for this market — people
// think in "I need ₦50,000", not "I'll sell 36.4 USDC".
//
// Flow: amount -> live routes -> payout bank -> create -> deposit address ->
// the user sends from their wallet -> the order screen takes over.
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
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import { getNigerianBanks, resolveAccountName, type NgBank } from '@/services/ngBanks';
import {
  createSell,
  getQuote,
  getSupportedBankCodes,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
} from '@/services/paycrestApi';

const QUOTE_DEBOUNCE_MS = 450;
/** Warn while the user can still act. An on-chain send needs gas, a signature
 *  and confirmations — more steps than a bank transfer, so warn earlier. */
const EXPIRY_WARNING_SECONDS = 420;
/** NUBAN length — resolve the account name only once it can possibly match. */
const NUBAN_LENGTH = 10;

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

const venueId = (v: PaycrestVenue) => `${v.token}:${v.chain}`;
const venueLabel = (v: PaycrestVenue) => `${v.token} on ${CHAIN_LABELS[v.chain] ?? v.chain}`;

export default function PaycrestSellScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  const { asset } = useLocalSearchParams<{ asset?: string }>();

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [routeOpen, setRouteOpen] = useState(false);

  // Payout destination.
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [bank, setBank] = useState<NgBank | null>(null);
  const [bankOpen, setBankOpen] = useState(false);
  const [accountNo, setAccountNo] = useState('');
  const [accountName, setAccountName] = useState('');
  const [resolving, setResolving] = useState(false);

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [creating, setCreating] = useState(false);

  const quoteSeq = useRef(0);

  // An offramp expires exactly like an onramp does. Sending after the window
  // closes puts crypto at a provider address for an order that no longer
  // exists — the same stranded-funds failure as a late bank transfer, and just
  // as unrecoverable, so the deadline has to be on screen.
  const { label: countdown, expired, secs } = useCountdown(order?.validUntil);
  const expiringSoon = !expired && secs > 0 && secs <= EXPIRY_WARNING_SECONDS;

  // Only banks Paycrest can actually pay out to. Showing the full national
  // list and failing at submit would waste the user's time on a choice that
  // was never available.
  useEffect(() => {
    (async () => {
      try {
        const [all, supported] = await Promise.all([
          getNigerianBanks(),
          getSupportedBankCodes('NGN'),
        ]);
        const allow = new Set(supported);
        setBanks(supported.length ? all.filter((b) => allow.has(b.code)) : all);
      } catch {
        // A failed intersection must not leave the user with no bank list at
        // all — fall back to the full one and let the server reject.
        try {
          setBanks(await getNigerianBanks());
        } catch {
          /* the picker simply stays empty */
        }
      }
    })();
  }, []);

  // Live quoting, same contract as the buy screen: a quote is only true for
  // the size it was asked about, and only for a moment.
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
        const q = await getQuote('sell', raw);
        if (seq !== quoteSeq.current) return;
        setQuote(q);
        setQuoteError(null);
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
        setQuoteError(
          e instanceof PaycrestError ? e.message : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === quoteSeq.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [amount, asset]);

  // Resolve the account name so the user sees who they are actually paying
  // before any crypto leaves their wallet.
  useEffect(() => {
    if (!bank || accountNo.length !== NUBAN_LENGTH) {
      setAccountName('');
      return;
    }
    let cancelled = false;
    setResolving(true);
    (async () => {
      try {
        const res = await resolveAccountName(bank.code, accountNo);
        if (cancelled) return;
        setAccountName(res.ok ? res.accountName : '');
      } catch {
        if (!cancelled) setAccountName('');
      } finally {
        if (!cancelled) setResolving(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [bank, accountNo]);

  const onConfirm = useCallback(async () => {
    if (!venue || !address || !bank) return;
    setCreating(true);
    try {
      const created = await createSell({
        amount: amount.trim(),
        chain: venue.chain,
        token: venue.token,
        walletAddress: address,
        bankCode: bank.code,
        accountNumber: accountNo,
        accountName,
      });
      setOrder(created);
    } catch (e) {
      Alert.alert(
        'Could not start your sale',
        e instanceof PaycrestError ? e.message : 'Please try again.',
      );
    } finally {
      setCreating(false);
    }
  }, [venue, address, bank, accountNo, accountName, amount]);

  const routeItems = useMemo(
    () =>
      (quote?.venues ?? []).map((v) => ({
        id: venueId(v),
        label: venueLabel(v),
        sublabel: `Send ${v.tokenAmount.toFixed(4)} ${v.token} · ${
          v.spreadPercent >= 0 ? '+' : ''
        }${v.spreadPercent.toFixed(2)}% vs market`,
      })),
    [quote],
  );

  const bankItems = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name })),
    [banks],
  );

  const ready =
    !!venue && !!address && !!bank && accountNo.length === NUBAN_LENGTH && !!accountName;

  // ── Deposit instructions ────────────────────────────────────────────────
  if (order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <Card>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              Send exactly
            </Text>
            <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
              {order.tokenAmount} {order.token}
            </Text>
            <Text variant="caption" style={{ color: colors.textMuted }}>
              on {CHAIN_LABELS[order.chain] ?? order.chain}, from your connected wallet.
            </Text>
          </Card>

          <Card
            style={{
              borderWidth: 1,
              borderColor: expired ? colors.danger : expiringSoon ? colors.warning : colors.border,
            }}
          >
            {expired ? (
              <>
                <Text variant="bodyMed" weight="700" style={{ color: colors.danger }}>
                  This order has expired
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Do not send to this address. Start a new sale instead.
                </Text>
              </>
            ) : (
              <>
                <Text
                  variant="bodySm"
                  style={{ color: expiringSoon ? colors.warning : colors.textSecondary }}
                >
                  Time left to send
                </Text>
                <Text
                  variant="title1"
                  weight="700"
                  style={{ color: expiringSoon ? colors.warning : colors.textPrimary }}
                >
                  {countdown}
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Sending after this runs out leaves your crypto at the provider with no order to
                  match it to.
                </Text>
              </>
            )}
          </Card>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              To this address
            </Text>
            {/* Full address, never truncated: a send to a mistyped address is
                irreversible and nothing on our side can recover it. */}
            <CopyableField value={order.depositAddress ?? ''} />
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              Send only {order.token} on {CHAIN_LABELS[order.chain] ?? order.chain}. A different
              token, or the right token on a different network, will not reach the provider.
            </Text>
          </Card>

          <SummaryCard>
            <SummaryRow label="You receive" value={`₦${order.amount}`} />
            <SummaryRow label="Into" value={order.payoutAccountName ?? ''} />
            <SummaryRow label="Account" value={order.payoutAccount ?? ''} />
            {order.rate ? <SummaryRow label="Rate" value={`₦${order.rate}`} /> : null}
          </SummaryCard>

          <Button
            title="I've sent it"
            onPress={() => router.replace(`/wallet/paycrest/order/${order.id}` as never)}
            disabled={expired}
            fullWidth
          />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount, route, payout ───────────────────────────────────────────────
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <AmountInput
            label="You receive"
            value={amount}
            onChangeText={setAmount}
            symbol="₦"
            symbolPosition="prefix"
            autoFocus
          />

          {quoting && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Spinner size="small" />
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Checking live rates…
              </Text>
            </View>
          )}

          {quoteError && !quoting && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
              <Text variant="bodySm" style={{ color: colors.warning }}>
                {quoteError}
              </Text>
            </Card>
          )}

          {quote && quote.venues.length > 0 && (
            <SelectRow
              label="You send"
              value={venue ? venueLabel(venue) : undefined}
              placeholder="Choose a network"
              onPress={() => setRouteOpen(true)}
            />
          )}

          <SelectRow
            label="Pay into"
            value={bank?.name}
            placeholder="Choose a bank"
            onPress={() => setBankOpen(true)}
          />
          <Input
            label="Account number"
            value={accountNo}
            onChangeText={(v: string) => setAccountNo(v.replace(/\D/g, '').slice(0, NUBAN_LENGTH))}
            keyboardType="number-pad"
            placeholder="0123456789"
          />
          {resolving && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Checking account…
            </Text>
          )}
          {!!accountName && (
            <Text variant="bodySm" weight="600" style={{ color: colors.success }}>
              {accountName}
            </Text>
          )}
          {!resolving && !accountName && accountNo.length === NUBAN_LENGTH && bank && (
            <Text variant="caption" style={{ color: colors.danger }}>
              We could not verify that account. Check the number and bank.
            </Text>
          )}

          {venue && (
            <SummaryCard>
              <SummaryRow
                label="You send"
                value={`${venue.tokenAmount.toFixed(6)} ${venue.token}`}
              />
              <SummaryRow label="Rate" value={`₦${venue.rate} / ${venue.token}`} />
              <SummaryRow
                label="Cost vs market"
                value={`${venue.spreadPercent >= 0 ? '+' : ''}${venue.spreadPercent.toFixed(2)}%`}
              />
            </SummaryCard>
          )}

          <Button
            title="Continue"
            onPress={onConfirm}
            loading={creating}
            disabled={!ready || quoting || creating}
            fullWidth
          />
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — you send the crypto yourself.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={routeOpen}
        onClose={() => setRouteOpen(false)}
        title="Choose a network"
        items={routeItems}
        selectedId={venue ? venueId(venue) : undefined}
        onSelect={(item) => {
          const picked = quote?.venues.find((v) => venueId(v) === item.id);
          if (picked) setVenue(picked);
          setRouteOpen(false);
        }}
        emptyText="No routes can fill this amount right now."
      />
      <PickerSheet
        visible={bankOpen}
        onClose={() => setBankOpen(false)}
        title="Choose a bank"
        items={bankItems}
        selectedId={bank?.code}
        searchable
        onSelect={(item) => {
          setBank(banks.find((b) => b.code === item.id) ?? null);
          setBankOpen(false);
        }}
        emptyText="No banks available."
      />
    </SafeAreaView>
  );
}
