// app/merchants/pay-links/new.tsx
//
// Request payment: a TSA Pay merchant makes a payment link and hands it to a
// customer — QR code, share sheet or copied link. It starts with the choice of
// link: a fixed amount for one customer (one-time), or one where the customer
// enters the amount and any number of customers can pay (reusable — a shop's
// counter QR). A made reusable link opens on its own screen, which is where its
// QR and payments live. Her links are live once she can take live payments,
// test links until then. The same form, words and share message as the web
// app's Payment links page.
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
  Button, Card, Input, RadioGroup, SegmentedControl, SummaryCard, SummaryRow, Text,
} from '@/components/ui';
import { NairaField } from '@/components/pay/NairaField';
import { PayLinkShare, TestLinkBanner } from '@/components/pay/PayLinkShare';
import { PAY_LINKS, PAY_SETTINGS } from '@/constants/routes';
import { OwnPayMerchantGate } from '@/components/pay/OwnPayMerchantGate';
import { makeIdempotencyKey } from '@/services/itApi';
import {
  createOpenLink, createPayLink, OWNER_CODES, payLinkModeOf, payLinkModeParam,
  type CreatePayLinkInput, type OwnMerchant, type PayLink, type PayLinkMode,
} from '@/services/payMerchantApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';
import {
  koboFromNairaInput, LINK_OPEN_DAYS, nairaInputError, openLinkRangeFrom, payLinkShareMessage, type LinkOpenDays,
} from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

const DAYS_LABEL: Record<LinkOpenDays, string> = { 1: '1 day', 3: '3 days', 7: '7 days' };

type LinkKind = 'once' | 'open';
const KINDS: { value: LinkKind; label: string; sublabel: string }[] = [
  { value: 'once', label: 'Fixed amount', sublabel: 'For one customer' },
  { value: 'open', label: 'Customer enters the amount', sublabel: 'Reusable — good for a shop QR' },
];

export default function NewPayLink() {
  const listMode = payLinkModeParam(useLocalSearchParams<{ listMode?: string }>().listMode);
  return (
    <OwnPayMerchantGate>
      {(merchant) => <NewLinkForm merchant={merchant} listMode={listMode} />}
    </OwnPayMerchantGate>
  );
}

function NewLinkForm({ merchant, listMode }: { merchant: OwnMerchant; listMode?: PayLinkMode }) {
  const { colors, spacing } = useTheme();

  const [kind, setKind] = useState<LinkKind>('once');
  const [amount, setAmount] = useState('');
  const [label, setLabel] = useState('');
  const [min, setMin] = useState('');
  const [max, setMax] = useState('');
  const [labelError, setLabelError] = useState<string | undefined>();
  const [rangeErrors, setRangeErrors] = useState<Partial<Record<'min' | 'max', string>>>({});
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

  const mode = testInstead ? 'test' : payLinkModeOf(merchant);

  const keyFor = (request: string) => {
    if (sentKey.current?.request !== request) sentKey.current = { request, key: makeIdempotencyKey() };
    return sentKey.current.key;
  };

  /** Sends one create; a refusal is said on the form. */
  const send = async (make: () => Promise<void>): Promise<void> => {
    submitting.current = true;
    setSaving(true);
    setFormError(undefined);
    setReferenceError(undefined);
    setRefusedWhy(undefined);
    try {
      await make();
      sentKey.current = null;
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

  const createOpen = async () => {
    const labelWhy = label.trim() ? undefined : 'Name it — customers see this, e.g. your shop and where it is.';
    const range = openLinkRangeFrom(min, max);
    setLabelError(labelWhy);
    setRangeErrors('errors' in range ? range.errors : {});
    if (labelWhy || 'errors' in range) return;
    const input = { label: label.trim(), ...range.range };
    const key = keyFor(JSON.stringify(['open', mode, input]));
    await send(async () => {
      const made = await createOpenLink(mode, input, key);
      // Its own screen is where its QR, share and payments live; back from it returns to where she came from.
      router.replace(`/merchants/pay-links/open/${made.id}?mode=${made.mode}`);
    });
  };

  const create = async () => {
    if (submitting.current) return;
    if (kind === 'open') { await createOpen(); return; }
    const kobo = koboFromNairaInput(amount);
    const amountWhy = amount.trim() ? nairaInputError(amount) : 'Enter the amount to request.';
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
    const key = keyFor(JSON.stringify(['once', mode, input]));
    await send(async () => {
      setCreated(await createPayLink(mode, input, key));
    });
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
          <PayLinkShare url={created.checkoutUrl} message={payLinkShareMessage(merchant.businessName, created)} />
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

        <RadioGroup<LinkKind> options={KINDS} value={kind} onChange={(k) => { setKind(k); setFormError(undefined); }} />

        {kind === 'open' ? (
          <>
            <Input
              label="Name"
              value={label}
              onChangeText={setLabel}
              placeholder="e.g. Mama Ngozi Store — Shop 12"
              maxLength={80}
              helper="Customers see this when they scan or open the link."
              error={labelError}
            />
            <NairaField label="Lowest amount (optional)" value={min} onChangeText={setMin} error={rangeErrors.min} />
            <NairaField
              label="Highest amount (optional)"
              value={max}
              onChangeText={setMax}
              error={rangeErrors.max}
              helper="Leave both blank to take any amount within your TSA Pay limits."
            />
          </>
        ) : (
          <>
            <NairaField label="Amount" value={amount} onChangeText={setAmount} error={amountError} />
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
              helper="Anyone with the link can see this name."
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
          </>
        )}

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
