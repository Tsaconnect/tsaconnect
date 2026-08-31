// app/wallet/it/buy-stable.tsx
//
// Buy USDT/USDC with Naira (brokered through Flutterwave; the platform holds
// no float, never custodies). The user pays into their own standing NGN
// virtual account; Flutterwave converts and delivers to a chain address WE
// resolve server-side from the user's own wallet.
//
// The backend allows only ONE unpaid stablecoin buy per user at a time, so
// this screen checks for an abandoned one before ever rendering the form —
// see docs/superpowers/specs/2026-08-31-stablecoin-buy-app-design.md.
//
// Flow: resume check → form (asset from route, naira amount, delivery chain)
// → create → one screen showing the estimate, the full delivery address, and
// the pay-into account → the user pays → "I've sent the money" (no API call)
// hands off to the intent detail screen, which the webhook drives from here.
import React, { useEffect, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams, router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  CopyableField,
  EmptyState,
  Icon,
  PickerSheet,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import {
  cancelStableBuy,
  createStableBuy,
  getStableBuyChains,
  listITIntents,
  makeIdempotencyKey,
  quoteStableBuy,
  IT_STABLE_BUY_ALREADY_OPEN,
  IT_STABLE_BUY_NO_VA,
  IT_STABLE_BUY_NO_WALLET,
  IT_STABLE_NOT_CONFIGURED,
  type ITIntent,
  type ITStableBuyQuote,
  type ITStableBuyResult,
  type StableAsset,
} from '@/services/itApi';
import { formatAssetUnits, formatMinorAsNaira } from '@/utils/stableBuyQuote';

export default function ITBuyStable() {
  const { colors, spacing, radius } = useTheme();

  const { asset: assetParam } = useLocalSearchParams<{ asset: string }>();
  const asset: StableAsset = assetParam === 'USDT' ? 'USDT' : 'USDC';

  // --- Step 0: resume check, before the form ever renders ------------------
  // The backend allows one unpaid stablecoin buy per user
  // (stable_buy_already_open). An abandoned one blocks buying until it
  // expires 24h later — cancelling below is the only self-service way out.
  const [checked, setChecked] = useState(false);
  const [resuming, setResuming] = useState<ITIntent | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [resumeError, setResumeError] = useState('');

  const checkResume = async () => {
    const res = await listITIntents();
    if (!res.success) return null;
    const open = (res.data?.intents ?? []).find(
      (i) =>
        i.direction === 'buy' &&
        (i.asset === 'USDC' || i.asset === 'USDT') &&
        i.status === 'pending_payment'
    );
    return open ?? null;
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const open = await checkResume();
      if (cancelled) return;
      setResuming(open);
      setChecked(true);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const onCancelResume = async () => {
    if (!resuming) return;
    setResumeError('');
    setCancelling(true);
    const res = await cancelStableBuy(resuming.id);
    setCancelling(false);
    if (!res.success) {
      setResumeError(res.message || 'Could not cancel. Try again.');
      return;
    }
    setResuming(null);
  };

  // --- Step 3: the form ------------------------------------------------------
  const [naira, setNaira] = useState('');
  const [chains, setChains] = useState<string[]>([]);
  const [chainsLoading, setChainsLoading] = useState(true);
  const [chain, setChain] = useState('');
  const [showChainPicker, setShowChainPicker] = useState(false);
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // Only chains this user actually holds a delivery address for come back —
  // a chain with no address is never in the list, so it can never be chosen.
  useEffect(() => {
    if (!checked || resuming) return;
    let cancelled = false;
    setChainsLoading(true);
    (async () => {
      const res = await getStableBuyChains(asset);
      if (cancelled) return;
      const list = res.success ? res.data?.chains ?? [] : [];
      setChains(list);
      setChain((prev) => prev || list[0] || '');
      setChainsLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [checked, resuming, asset]);

  // --- Step 4/5: create, then one screen with the estimate + full address +
  // pay-into account. The delivery address does not exist until the intent
  // does, so this can only ever run after a successful create. ---------------
  const [quote, setQuote] = useState<ITStableBuyQuote | null>(null);
  const [result, setResult] = useState<ITStableBuyResult | null>(null);

  // Mint ONE idempotency key per submitted form snapshot and reuse it for
  // every retry of that same create — a retried create must not mint a
  // second purchase. Editing the amount or chain invalidates it.
  const idempotencyKeyRef = useRef<string | null>(null);
  const resetKey = () => {
    idempotencyKeyRef.current = null;
  };

  const onNairaChange = (v: string) => {
    setNaira(v);
    setError('');
    resetKey();
  };
  const onChainSelect = (c: string) => {
    setChain(c);
    setShowChainPicker(false);
    resetKey();
  };

  const applyErrorCode = async (code?: string, message?: string) => {
    switch (code) {
      case IT_STABLE_BUY_ALREADY_OPEN: {
        // Step 0 should have caught this; fall back to the resume view.
        const open = await checkResume();
        setResuming(open);
        return;
      }
      case IT_STABLE_BUY_NO_WALLET:
      case IT_STABLE_BUY_NO_VA:
        setError('Finish setting up your wallet to buy.');
        return;
      case IT_STABLE_NOT_CONFIGURED:
        setError('Buying is temporarily unavailable.');
        return;
      default:
        setError(message || 'Something went wrong.');
    }
  };

  const submit = async () => {
    setError('');
    const amt = Number(naira);
    if (!amt || amt <= 0) {
      setError('Enter an amount');
      return;
    }
    if (!chain) {
      setError('Select a chain to receive on');
      return;
    }
    const amountMinor = String(Math.round(amt * 100));
    if (!idempotencyKeyRef.current) idempotencyKeyRef.current = makeIdempotencyKey();

    setSubmitting(true);
    const q = await quoteStableBuy(asset, amountMinor, 'NGN');
    if (!q.success || !q.data) {
      setSubmitting(false);
      await applyErrorCode(q.code, q.message);
      return;
    }
    const res = await createStableBuy(asset, amountMinor, 'NGN', chain, idempotencyKeyRef.current);
    setSubmitting(false);
    if (!res.success || !res.data) {
      await applyErrorCode(res.code, res.message);
      return;
    }
    setQuote(q.data);
    setResult(res.data);
  };

  // ---------------------------------------------------------------------------

  if (!checked) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner />
      </SafeAreaView>
    );
  }

  if (resuming) {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
              You have a pending purchase
            </Text>
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginBottom: spacing.md }}>
              Finish or cancel it before starting a new one.
            </Text>
            <SummaryCard>
              <SummaryRow label="Asset" value={resuming.asset || asset} />
              <SummaryRow label="You pay" value={`₦${formatMinorAsNaira(resuming.ngnKobo)}`} />
              <SummaryRow label="Status" value="Awaiting your transfer" />
            </SummaryCard>
            {resumeError ? (
              <Text variant="bodySm" style={{ color: colors.danger, marginTop: spacing.md }}>{resumeError}</Text>
            ) : null}
          </Card>
          <Button title="View purchase" onPress={() => router.push(`/wallet/it/intent/${resuming.id}`)} />
          <Button title="Cancel purchase" variant="secondary" loading={cancelling} onPress={onCancelResume} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  if (result && quote) {
    const destChain = result.intent.destChain || chain;
    const destAddress = result.intent.destAddress || '';
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.sm }}>
              Pay into this account
            </Text>
            <SummaryCard>
              <SummaryRow
                label="You'll receive"
                value={`${quote.indicative ? '≈ ' : ''}${formatAssetUnits(quote.assetUnits)} ${quote.asset}`}
              />
              <SummaryRow label="On" value={destChain} />
            </SummaryCard>

            {/* FULL address, never truncated, and never inside SummaryRow
                (which ellipsises). Truncation defeats the only purpose this
                line has: address-poisoning attacks generate an address whose
                first and last characters match the victim's, so a shortened
                "0x742d…9f2A" verifies as correct against a completely
                different address. This screen is the only place a wrong
                address can be caught — the server cannot verify it. */}
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginTop: spacing.md }}>
              Sent to your wallet
            </Text>
            <Text
              selectable
              numberOfLines={0}
              style={{
                marginTop: spacing.xs,
                color: colors.textPrimary,
                fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
                fontSize: 13,
                lineHeight: 18,
              }}
            >
              {destAddress || '—'}
            </Text>
            <Text variant="caption" style={{ color: colors.danger, marginTop: spacing.xs }}>
              Delivery is irreversible. Check this is your address.
            </Text>

            <SummaryCard>
              <SummaryRow label="You pay" value={`₦${formatMinorAsNaira(quote.fiatMinor)}`} />
            </SummaryCard>

            <View style={{ marginTop: spacing.md }}>
              <SummaryCard>
                <SummaryRow label="Bank" value={result.payInto.bankName || '—'} />
                <SummaryRow label="Account name" value={result.payInto.accountName || '—'} />
              </SummaryCard>
              <View style={{ marginTop: spacing.xs }}>
                <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                  Account number
                </Text>
                <CopyableField value={result.payInto.accountNumber || ''} />
              </View>
            </View>
          </Card>

          {/* No API call — the webhook drives everything from here. This
              button must not imply the app has been told anything. */}
          <Button
            title="I've sent the money"
            onPress={() => router.replace(`/wallet/it/intent/${result.intent.id}`)}
          />
        </ScrollView>
      </SafeAreaView>
    );
  }

  const chainLabel = chainsLoading ? 'Loading…' : chain || '—';

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
          <Text variant="bodySm" style={{ color: colors.textMuted }}>
            Buy {asset} with a Naira bank transfer into your permanent account. Choose the chain you
            want it delivered on.
          </Text>

          <SelectRow label="Asset" value={asset} leftIcon="wallet" onPress={() => {}} disabled />

          {!chainsLoading && chains.length === 0 ? (
            <EmptyState
              icon="wallet"
              title="No delivery address on file"
              subtitle={`Set up a wallet address before buying ${asset}.`}
            />
          ) : (
            <>
              <AmountInput
                label="Amount"
                value={naira}
                onChangeText={onNairaChange}
                symbol="₦"
                symbolPosition="prefix"
              />
              <SelectRow
                label="Deliver on"
                value={chainLabel}
                leftIcon="wallet"
                disabled={chainsLoading || chains.length === 0}
                onPress={() => setShowChainPicker(true)}
              />
              {error ? (
                <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text>
              ) : null}
              <Button
                title="Continue"
                loading={submitting}
                disabled={submitting || chainsLoading || chains.length === 0}
                onPress={submit}
              />
            </>
          )}

          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-start',
              gap: spacing.sm,
              backgroundColor: colors.infoBg,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Icon name="info" size={16} color={colors.info} />
            <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
              Delivery starts once your transfer is confirmed and the rate is priced at settlement.
            </Text>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={showChainPicker}
        onClose={() => setShowChainPicker(false)}
        title="Deliver on"
        items={chains.map((c) => ({ id: c, label: c, icon: 'wallet' as const }))}
        selectedId={chain}
        onSelect={(item) => onChainSelect(item.id)}
      />
    </SafeAreaView>
  );
}
