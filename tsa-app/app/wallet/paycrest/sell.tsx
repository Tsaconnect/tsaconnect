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
import { useRampCurrency } from '@/hooks/useRampCurrency';
import { resolveAccountName } from '@/services/ngBanks';
import {
  accountNumberMaxLength,
  isDigitsOnlyAccountCurrency,
  isValidAccountNumber,
  sanitizeAccountNumberInput,
} from '@/utils/payoutAccount';
import {
  claimPayment,
  createSell,
  getPayoutInstitutions,
  getQuote,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type PayoutInstitution,
} from '@/services/paycrestApi';

const QUOTE_DEBOUNCE_MS = 450;
/** Warn while the user can still act. An on-chain send needs gas, a signature
 *  and confirmations — more steps than a bank transfer, so warn earlier. */
const EXPIRY_WARNING_SECONDS = 420;

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
  const { asset, currency: currencyParam } = useLocalSearchParams<{
    asset?: string;
    currency?: string;
  }>();

  // The currency this order settles in — NOT the display currency. See
  // hooks/useRampCurrency.ts: deriving this from CurrencyContext's display
  // preference is the exact regression documented at wallet/it/index.tsx (a
  // user who switched display to USD lost the working NGN ramp). Restricted
  // to 'paycrest' so the picker can never offer a currency this screen would
  // fail to quote.
  const {
    code: currencyCode,
    currency: currencyConfig,
    setCode: setCurrencyCode,
    options: currencyOptions,
  } = useRampCurrency('paycrest');
  const symbol = currencyConfig.symbol;
  useEffect(() => {
    if (currencyParam) setCurrencyCode(currencyParam);
    // One-time sync of the hub's choice on arrival — see the identical note
    // on the buy screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currencyParam]);
  const [currencyPickerOpen, setCurrencyPickerOpen] = useState(false);

  // resolveAccountName is a Nigerian NUBAN lookup — it cannot resolve an
  // account in any other currency's country, so gate both the lookup and the
  // submit requirement on NGN or sell becomes permanently stuck outside it.
  const requiresNameCheck = currencyCode === 'NGN';

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [routeOpen, setRouteOpen] = useState(false);

  // Payout destination.
  const [banks, setBanks] = useState<PayoutInstitution[]>([]);
  const [bank, setBank] = useState<PayoutInstitution | null>(null);
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

  // The server returns the list already in the right vocabulary for this
  // currency, so there is nothing to intersect. Outside Nigeria there is no
  // local list to intersect WITH. The previously selected bank is cleared on
  // a currency change — it belongs to the old currency's list and may not
  // even exist in the new one.
  useEffect(() => {
    setBank(null);
    getPayoutInstitutions(currencyCode)
      .then(setBanks)
      .catch(() => setBanks([]));
  }, [currencyCode]);

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
        const q = await getQuote('sell', raw, currencyCode);
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
  }, [amount, asset, currencyCode]);

  // Resolve the account name so the user sees who they are actually paying
  // before any crypto leaves their wallet.
  useEffect(() => {
    if (!requiresNameCheck || !bank || !isValidAccountNumber(currencyCode, accountNo)) {
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
  }, [requiresNameCheck, bank, accountNo, currencyCode]);

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
        currency: currencyCode,
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
  }, [venue, address, bank, accountNo, accountName, amount, currencyCode]);

  const routeItems = useMemo(
    () =>
      (quote?.venues ?? []).map((v) => ({
        id: venueId(v),
        label: venueLabel(v),
        sublabel: `Send ${v.tokenAmount.toFixed(4)} ${v.token}`,
      })),
    [quote],
  );

  const bankItems = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name })),
    [banks],
  );

  const ready =
    !!venue &&
    !!address &&
    !!bank &&
    isValidAccountNumber(currencyCode, accountNo) &&
    (!requiresNameCheck || !!accountName);

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
            <SummaryRow label="You receive" value={`${symbol}${order.amount}`} />
            <SummaryRow label="Into" value={order.payoutAccountName ?? ''} />
            <SummaryRow label="Account" value={order.payoutAccount ?? ''} />
            {order.rate ? <SummaryRow label="Rate" value={`${symbol}${order.rate}`} /> : null}
          </SummaryCard>

          <Button
            title="I've sent it"
            onPress={() => {
              // Tell the server the user asserts they sent the deposit — the
              // only signal that lets an order which later expires with
              // amountPaid=0 read as a genuine unmatched-deposit alarm
              // instead of an ordinary abandoned checkout. Fire-and-forget:
              // the user has already sent it, so a failed claim must not
              // block navigation — it just means this order degrades to a
              // plain "expired" if it never matches, the same as before.
              claimPayment(order.id).catch(() => {});
              router.replace(`/wallet/paycrest/order/${order.id}` as never);
            }}
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
          {/* Only offer a choice when there is one — see the identical note
              on the buy screen. */}
          {currencyOptions.length > 1 && (
            <SelectRow
              label="Currency"
              value={currencyCode}
              onPress={() => setCurrencyPickerOpen(true)}
            />
          )}

          <AmountInput
            label="You receive"
            value={amount}
            onChangeText={setAmount}
            symbol={symbol}
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
            onChangeText={(v: string) => setAccountNo(sanitizeAccountNumberInput(currencyCode, v))}
            keyboardType={isDigitsOnlyAccountCurrency(currencyCode) ? 'number-pad' : 'default'}
            placeholder={isDigitsOnlyAccountCurrency(currencyCode) ? '0123456789' : 'Account number'}
            maxLength={accountNumberMaxLength(currencyCode)}
          />
          {requiresNameCheck && resolving && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Checking account…
            </Text>
          )}
          {requiresNameCheck && !!accountName && (
            <Text variant="bodySm" weight="600" style={{ color: colors.success }}>
              {accountName}
            </Text>
          )}
          {requiresNameCheck &&
            !resolving &&
            !accountName &&
            isValidAccountNumber(currencyCode, accountNo) &&
            bank && (
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
              <SummaryRow label="Rate" value={`${symbol}${venue.rate} / ${venue.token}`} />
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
        visible={currencyPickerOpen}
        onClose={() => setCurrencyPickerOpen(false)}
        title="Currency"
        items={currencyOptions}
        selectedId={currencyCode}
        onSelect={(item) => {
          setCurrencyCode(item.id);
          // A new currency means a fresh route and payout destination — the
          // ones held over would either belong to the wrong currency's quote
          // or (for the bank/account) simply not exist in the new list.
          setAmount('');
          setVenue(null);
          setAccountNo('');
          setAccountName('');
          setCurrencyPickerOpen(false);
        }}
        emptyText="No currencies available."
      />
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
