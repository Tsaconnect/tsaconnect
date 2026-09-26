// app/merchants/pay-links/new.tsx
//
// Request payment: a TSA Pay merchant makes a payment link for an amount and
// hands it to a customer — QR code, share sheet or copied link. Her link is
// live once her business is switched live, a test link until then. The same
// form, words and share message as the web app's Payment links page.
//
// Opened from the list (`?listMode=`), "See all payment links" goes back to it
// rather than stacking a second list — unless the link was made in the other mode.
//
// A retry of the same form is the same request: it carries the same
// Idempotency-Key, so a create that timed out but landed answers with that
// link instead of making a second. Any change to the form is a new request.
import React, { useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button, Card, ErrorState, Input, SegmentedControl, Spinner, SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import { PayLinkShare, TestLinkBanner } from '@/components/pay/PayLinkShare';
import { PAY_LINKS, PAY_SETTINGS } from '@/constants/routes';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import { makeIdempotencyKey } from '@/services/itApi';
import {
  createPayLink, OWNER_CODES, payLinkModeOf, payLinkModeParam, type CreatePayLinkInput, type PayLink,
} from '@/services/payMerchantApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';
import { koboFromNairaInput, LINK_OPEN_DAYS, type LinkOpenDays } from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

const DAYS_LABEL: Record<LinkOpenDays, string> = { 1: '1 day', 3: '3 days', 7: '7 days' };

export default function NewPayLink() {
  const { colors, spacing } = useTheme();
  const { load, reload } = useOwnPayMerchant();
  const listMode = payLinkModeParam(useLocalSearchParams<{ listMode?: string }>().listMode);

  const [amount, setAmount] = useState('');
  const [description, setDescription] = useState('');
  const [customerName, setCustomerName] = useState('');
  const [reference, setReference] = useState('');
  const [days, setDays] = useState<LinkOpenDays>(7);
  // The server refused a live link (live is off for her): she chose a test one instead.
  const [testInstead, setTestInstead] = useState(false);

  const [amountError, setAmountError] = useState<string | undefined>();
  const [descriptionError, setDescriptionError] = useState<string | undefined>();
  const [formError, setFormError] = useState<string | undefined>();
  const [referenceError, setReferenceError] = useState<string | undefined>();
  const [refusedWhy, setRefusedWhy] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);
  const [created, setCreated] = useState<PayLink | null>(null);
  // A double tap must not make two links.
  const submitting = useRef(false);
  const sentKey = useRef<{ request: string; key: string } | null>(null);

  if (load.kind === 'loading') {
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }
  if (load.kind === 'error') {
    return <ErrorState title="Could not load your TSA Pay account" subtitle={load.message} onRetry={reload} />;
  }
  if (load.kind === 'none') {
    return <ErrorState title="No TSA Pay account" subtitle="Apply to accept payments with TSA Pay first." />;
  }
  const { merchant } = load;
  const mode = testInstead ? 'test' : payLinkModeOf(merchant);

  const keyFor = (request: string) => {
    if (sentKey.current?.request !== request) sentKey.current = { request, key: makeIdempotencyKey() };
    return sentKey.current.key;
  };

  const create = async () => {
    if (submitting.current) return;
    const kobo = koboFromNairaInput(amount);
    const amountWhy = kobo !== null ? undefined
      : !amount.trim() ? 'Enter the amount to request.'
        : 'Enter naira with a point for kobo, like 5000 or 5000.50.';
    const descriptionWhy = description.trim() ? undefined : "Say what it's for — your customer sees it.";
    setAmountError(amountWhy);
    setDescriptionError(descriptionWhy);
    if (kobo === null || descriptionWhy) return;
    const input: CreatePayLinkInput = {
      amount: kobo,
      description: description.trim(),
      customerName: customerName.trim() || undefined,
      reference: reference.trim() || undefined,
      expiresInDays: days,
    };
    submitting.current = true;
    setSaving(true);
    setFormError(undefined);
    setReferenceError(undefined);
    setRefusedWhy(undefined);
    try {
      const link = await createPayLink(mode, input, keyFor(JSON.stringify([mode, input])));
      sentKey.current = null;
      setCreated(link);
    } catch (e) {
      const why = errorCode(e);
      const message = errorMessage(e, 'Could not create the link. Please try again.');
      if (why === OWNER_CODES.REFERENCE_TAKEN) setReferenceError(message);
      else setFormError(message);
      setRefusedWhy(why);
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  const another = () => {
    setCreated(null);
    setAmount('');
    setDescription('');
    setCustomerName('');
    setReference('');
    setDays(7);
  };

  const seeAll = (made: PayLink) => {
    if (listMode === made.mode) router.back();
    else router.replace(`${PAY_LINKS}?mode=${made.mode}`);
  };

  if (created) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
          {created.mode === 'test' ? <TestLinkBanner /> : null}
          <Card>
            <SummaryCard>
              <SummaryRow label="Amount" value={formatKobo(String(created.amount))} total />
              {/* The share message quotes what the customer pays, so she sees it here first. */}
              {created.customerTotal !== created.amount ? (
                <SummaryRow label="Your customer pays" value={formatKobo(String(created.customerTotal))} />
              ) : null}
              <SummaryRow label="You'll receive" value={formatKobo(String(created.merchantNet))} />
              <SummaryRow label="For" value={created.description} />
              {created.customerName ? <SummaryRow label="Customer" value={created.customerName} /> : null}
            </SummaryCard>
          </Card>
          <PayLinkShare link={created} businessName={merchant.businessName} />
          <Button title="Request another" variant="secondary" onPress={another} />
          <Button title="See all payment links" variant="ghost" onPress={() => seeAll(created)} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }} keyboardShouldPersistTaps="handled">
        {mode === 'test' ? <TestLinkBanner /> : null}

        {/* A plain field, not AmountInput: that one strips a comma, and "5000,50" must be refused, not read as 500,050. */}
        <Input
          label="Amount"
          value={amount}
          onChangeText={setAmount}
          placeholder="0.00"
          keyboardType="decimal-pad"
          leftAdornment={<Text variant="bodyMed" weight="700" color="textSecondary">₦</Text>}
          error={amountError}
        />
        <Input
          label="What it's for"
          value={description}
          onChangeText={setDescription}
          placeholder="e.g. 2 bags of rice"
          maxLength={255}
          helper="Your customer sees this."
          error={descriptionError}
        />
        <Input
          label="Customer name (optional)"
          value={customerName}
          onChangeText={setCustomerName}
          placeholder="Who you're asking"
          maxLength={100}
        />
        <Input
          label="Your reference (optional)"
          value={reference}
          onChangeText={setReference}
          placeholder="e.g. INV-104"
          autoCapitalize="none"
          autoCorrect={false}
          helper="Leave blank and we'll make one"
          error={referenceError}
        />
        <View style={{ gap: spacing.xs }}>
          <Text variant="bodySm" weight="600" color="textSecondary">Link open for</Text>
          <SegmentedControl<string>
            segments={LINK_OPEN_DAYS.map((d) => ({ key: String(d), label: DAYS_LABEL[d] }))}
            value={String(days)}
            onChange={(k) => setDays(Number(k) as LinkOpenDays)}
          />
        </View>

        {formError ? <Text variant="bodySm" style={{ color: colors.danger }}>{formError}</Text> : null}
        {refusedWhy === OWNER_CODES.SETTLEMENT_NOT_SET ? (
          <Button title="Set where you're paid" variant="secondary" onPress={() => router.push(PAY_SETTINGS)} />
        ) : null}
        {refusedWhy === OWNER_CODES.LIVE_NOT_ENABLED && mode === 'live' ? (
          <Button
            title="Make a test link instead"
            variant="secondary"
            onPress={() => { setTestInstead(true); setFormError(undefined); setRefusedWhy(undefined); }}
          />
        ) : null}
        <Button title="Create link" onPress={create} loading={saving} disabled={saving} />
      </ScrollView>
    </SafeAreaView>
  );
}
