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
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
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
  IT_STABLE_BUY_ALREADY_OPEN,
  IT_STABLE_BUY_NO_VA,
  IT_STABLE_BUY_NO_WALLET,
  IT_STABLE_NOT_CONFIGURED,
  type ITIntent,
  type ITStableBuyResult,
  type StableAsset,
} from '@/services/itApi';
import { formatAssetUnits, formatMinorAsNaira } from '@/utils/stableBuyQuote';
import { promptKycRefusal } from '@/utils/kycRefusal';
import { PayerInstructionNotice } from '@/components/common/PayerInstructionNotice';
import { payerInstructionOf } from '@/utils/payInto';

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

  const cancelResume = async () => {
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

  // This is the more dangerous of the two cancel affordances, because it is
  // the one a returning buyer lands on: they paid, backgrounded the app,
  // reopened Buy, and this screen greets them with the button. With no open
  // buy intent left, the incoming charge no longer settles this purchase and
  // the naira is auto-credited as a DIFFERENT asset — so the risk gets named,
  // and the tap gets confirmed.
  const onCancelResume = () => {
    Alert.alert(
      'Cancel this purchase?',
      'If you have already sent the transfer, cancelling can route your money to a different asset. Only cancel if you have not paid.',
      [
        { text: 'Keep purchase', style: 'cancel' },
        { text: 'Cancel purchase', style: 'destructive', onPress: () => { void cancelResume(); } },
      ]
    );
  };

  // --- Step 3: the form ------------------------------------------------------
  const [naira, setNaira] = useState('');
  const [chains, setChains] = useState<string[]>([]);
  const [chainsLoading, setChainsLoading] = useState(true);
  const [chain, setChain] = useState('');
  const [showChainPicker, setShowChainPicker] = useState(false);
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [chainsError, setChainsError] = useState('');

  // Only chains this user actually holds a delivery address for come back —
  // a chain with no address is never in the list, so it can never be chosen.
  //
  // A FAILED fetch is not an empty one. Collapsing both to [] rendered "No
  // delivery address on file" — false, and actionable-looking — for a dropped
  // connection, a 500, or stable_not_configured, on the very first call this
  // screen makes. The empty state is reserved for a list the server actually
  // returned as empty; everything else is an error with a retry.
  const chainsReqRef = useRef(0);
  const loadChains = useCallback(async () => {
    const gen = ++chainsReqRef.current;
    setChainsLoading(true);
    setChainsError('');
    const res = await getStableBuyChains(asset);
    if (gen !== chainsReqRef.current) return;
    if (!res.success) {
      setChains([]);
      setChainsError(
        res.code === IT_STABLE_NOT_CONFIGURED || res.status === 503
          ? 'Buying is temporarily unavailable. Try again shortly.'
          : "We couldn't load the chains you can receive on."
      );
      setChainsLoading(false);
      return;
    }
    const list = res.data?.chains ?? [];
    setChains(list);
    setChain((prev) => prev || list[0] || '');
    setChainsLoading(false);
  }, [asset]);

  useEffect(() => {
    if (!checked || resuming) return;
    void loadChains();
    // Invalidate the in-flight request on unmount or a dependency change, so a
    // late response cannot overwrite newer state.
    return () => {
      chainsReqRef.current += 1;
    };
  }, [checked, resuming, loadChains]);

  // --- Step 4/5: create, then one screen with the estimate + full address +
  // pay-into account. The delivery address does not exist until the intent
  // does, so this can only ever run after a successful create. ---------------
  //
  // The confirm screen displays result.intent.assetAmount / .ngnKobo, NOT a
  // client-side quote. PostStableBuy re-quotes independently server-side
  // (it_handler.go PostStableBuy calls QuoteStableBuy itself) and
  // CreateStableBuyIntent writes AssetAmount from THAT quote — a separate
  // client quoteStableBuy call, sequenced before create, prices a DIFFERENT
  // moment and can disagree with what actually got stored on the intent
  // under rate movement. Reading the intent is the only way this screen and
  // the progress screen a moment later can ever show the same number.
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
        // Step 0 should have caught this; fall back to the resume view. It can
        // legitimately come back empty — listITIntents can fail, and the open
        // intent can fall outside the server's page limit — and `submit`
        // cleared `error` on entry, so without this the spinner would just stop
        // and nothing at all would happen.
        const open = await checkResume();
        if (!open) {
          setError(
            'You already have a purchase awaiting payment. Open it from your order history to pay or cancel it.'
          );
          return;
        }
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
        if (promptKycRefusal(code, message)) return;
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
    const res = await createStableBuy(asset, amountMinor, 'NGN', chain, idempotencyKeyRef.current);
    setSubmitting(false);
    if (!res.success || !res.data) {
      await applyErrorCode(res.code, res.message);
      return;
    }
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
              Finish or cancel it before starting a new one. You can cancel this purchase before
              paying. If you have already sent the transfer, do not cancel — wait for it to be
              matched instead.
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

  if (result) {
    const destChain = result.intent.destChain || chain;
    const destAddress = result.intent.destAddress || '';
    const gotAsset = result.intent.asset || asset;
    const payerInstruction = payerInstructionOf(result.payInto);
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.sm }}>
              Pay into this account
            </Text>
            <SummaryCard>
              {/* Always an estimate: the binding price is struck when the
                  naira lands, so even the amount just written to the intent
                  is provisional until settlement. */}
              <SummaryRow
                label="You'll receive"
                value={`≈ ${formatAssetUnits(result.intent.assetAmount || '0')} ${gotAsset}`}
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

            {/* "exactly", not a bare "You pay": settleStableBuyFromCharge
                prices whatever actually lands and fails closed on an
                underpayment below the provider floor, parking the intent in
                buy_awaiting_rate — an operator-only state with no user-facing
                recovery (spec §10). */}
            <SummaryCard>
              <SummaryRow
                label="Transfer exactly"
                value={`₦${formatMinorAsNaira(result.intent.ngnKobo)}`}
              />
            </SummaryCard>
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
              Sending a different amount can hold your purchase for review.
            </Text>

            <View style={{ marginTop: spacing.md }}>
              {payerInstruction ? (
                <View style={{ marginBottom: spacing.sm }}>
                  <PayerInstructionNotice instruction={payerInstruction} />
                </View>
              ) : null}
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

          {chainsError ? (
            <View style={{ gap: spacing.md }}>
              <Text variant="bodySm" style={{ color: colors.danger }}>{chainsError}</Text>
              <Button title="Retry" variant="secondary" loading={chainsLoading} onPress={loadChains} />
            </View>
          ) : !chainsLoading && chains.length === 0 ? (
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
