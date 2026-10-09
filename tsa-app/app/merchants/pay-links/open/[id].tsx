// app/merchants/pay-links/open/[id].tsx
//
// One reusable payment link — the customer enters the amount, and any number of
// customers can pay through it (a shop's counter QR). Here its owner shares or
// copies it (big QR), sees whether it is taking payments (and why not), what
// she has received through it and the latest payments, edits its name and
// range, pauses and resumes it, and replaces it: a new address and QR for the
// same link, and the old address — with every poster printed from it — stops
// working, so that asks first. Posters are printed from the web dashboard.
// Refreshed every 15 s while she looks.
import React, { useCallback, useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AccentCard, Button, Card, ErrorState, Input, SegmentedControl, Sheet, StatusPill, SummaryCard, SummaryRow, Text,
  useToast,
} from '@/components/ui';
import { NairaField } from '@/components/pay/NairaField';
import { PayLinkShare, TestLinkBanner } from '@/components/pay/PayLinkShare';
import { COMPANY } from '@/constants/legal';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { OwnPayMerchantGate, ScreenSpinner } from '@/components/pay/OwnPayMerchantGate';
import {
  getOpenLink, payLinkModeOf, payLinkModeParam, replaceOpenLink, updateOpenLink,
  type OpenLink, type OpenLinkChange, type PayLink, type PayLinkMode,
} from '@/services/payMerchantApi';
import { errorMessage } from '@/utils/payCheckout';
import {
  formatLinkTime, nairaFieldText, openLinkAmountRule, openLinkNotTakingWhy, openLinkPaymentExpiryOptions,
  openLinkRangeFrom, openLinkReceived, openLinkShareMessage, openLinkStatus, openPaymentPurpose, payLinkStatus,
} from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

const QR_SIZE = 240;
const POLL_MS = 15_000;
const WEB_DASHBOARD = COMPANY.webApp.replace(/^https:\/\//, '');

/**
 * The link as it stands, as a change: what Pause/Resume sends with only `active`
 * moved. paymentExpiresIn is left out, so Pause/Resume never touches it.
 */
const changeOf = (l: OpenLink): OpenLinkChange => ({
  label: l.label, minAmount: l.minAmount, maxAmount: l.maxAmount, active: l.active,
});

type EditForm = { label: string; min: string; max: string; paymentExpiresIn: number };

export default function OpenLinkDetail() {
  const { id, mode: modeParam } = useLocalSearchParams<{ id: string; mode?: string }>();
  return (
    <OwnPayMerchantGate>
      {(merchant) => (
        // The list opens a link in its own mode; opened cold, a link is in the mode she is in.
        <LinkDetail
          id={String(id)}
          mode={payLinkModeParam(modeParam) ?? payLinkModeOf(merchant)}
          businessName={merchant.businessName}
          suspended={merchant.status === 'suspended'}
        />
      )}
    </OwnPayMerchantGate>
  );
}

function LinkDetail({ id, mode, businessName, suspended }: {
  id: string; mode: PayLinkMode; businessName: string; suspended: boolean;
}) {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [link, setLink] = useState<OpenLink | null>(null);
  const [payments, setPayments] = useState<PayLink[]>([]);
  const [error, setError] = useState<string | undefined>();

  const [form, setForm] = useState<EditForm | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Partial<Record<'label' | 'min' | 'max', string>>>({});
  const [confirmingReplace, setConfirmingReplace] = useState(false);
  const [sheetError, setSheetError] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);
  // A double tap must send one change.
  const submitting = useRef(false);

  // Only the latest read counts: a poll that set off before a change landed
  // must not put the link back as it was.
  const seq = useRef(0);
  const refresh = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const got = await getOpenLink(mode, id);
      if (mine !== seq.current) return;
      setLink(got.openLink);
      setPayments(got.payments ?? []);
      setError(undefined);
    } catch (e) {
      if (mine !== seq.current) return;
      setError(errorMessage(e, 'Could not load this payment link.'));
    }
  }, [id, mode]);
  // Payments arrive while she watches (a counter QR is paid all day).
  useFocusPolling(refresh, POLL_MS);

  /** Runs one change against the server; true when it landed. */
  const run = async (send: () => Promise<OpenLink>, onError: (message: string) => void): Promise<boolean> => {
    if (submitting.current) return false;
    submitting.current = true;
    setSaving(true);
    try {
      const next = await send();
      seq.current += 1;
      setLink(next);
      return true;
    } catch (e) {
      onError(errorMessage(e, 'Could not save this payment link. Please try again.'));
      // Changed elsewhere since this screen read it: show it as it now is.
      if ((e as { status?: number } | null)?.status === 409) void refresh();
      return false;
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  if (link === null) {
    if (error) return <ErrorState title="Could not load this payment link" subtitle={error} onRetry={refresh} />;
    return <ScreenSpinner />;
  }

  const setActive = async (active: boolean) => {
    if (await run(() => updateOpenLink(mode, link.id, { ...changeOf(link), active }, link.version), (m) => toast.error(m))) {
      toast.success(active ? 'Link resumed' : 'Link paused');
    }
  };

  const saveEdit = async () => {
    if (!form) return;
    const range = openLinkRangeFrom(form.min, form.max);
    const errors = {
      ...('errors' in range ? range.errors : {}),
      ...(form.label.trim() ? {} : { label: 'Name it — customers see this.' }),
    };
    setFieldErrors(errors);
    if ('errors' in range || errors.label) return;
    setSheetError(undefined);
    const change = { ...changeOf(link), label: form.label.trim(), ...range.range, paymentExpiresIn: form.paymentExpiresIn };
    if (await run(() => updateOpenLink(mode, link.id, change, link.version), setSheetError)) {
      setForm(null);
      toast.success('Link saved');
    }
  };

  const replace = async () => {
    setSheetError(undefined);
    if (await run(() => replaceOpenLink(mode, link.id), setSheetError)) {
      setConfirmingReplace(false);
      toast.success('New link ready — print the new poster');
    }
  };

  const notTakingWhy = openLinkNotTakingWhy(link);
  const openEdit = () => {
    setForm({
      label: link.label,
      min: nairaFieldText(link.minAmount),
      max: nairaFieldText(link.maxAmount),
      // An older server sends none: no expiry.
      paymentExpiresIn: link.paymentExpiresIn ?? 0,
    });
    setFieldErrors({});
    setSheetError(undefined);
  };
  const edit = (patch: Partial<EditForm>) => setForm((f) => (f ? { ...f, ...patch } : f));

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        {link.mode === 'test' ? <TestLinkBanner /> : null}
        {notTakingWhy ? (
          <AccentCard railColor={colors.warning} padding="md">
            <View style={{ gap: spacing.sm }}>
              <Text variant="bodySm" weight="700">Not taking payments</Text>
              <Text variant="bodySm">{notTakingWhy}</Text>
              {/* The one reason only she can fix from here: her range no longer fits her limits. */}
              {link.notTakingReason === 'range_outside_limits' && !suspended ? (
                <Button title="Edit the link" variant="secondary" size="sm" onPress={openEdit} />
              ) : null}
            </View>
          </AccentCard>
        ) : null}

        <PayLinkShare url={link.url} message={openLinkShareMessage(businessName, link)} qrSize={QR_SIZE}>
          <View style={{ alignItems: 'center', gap: spacing.xs }}>
            <Text variant="title3" weight="700" align="center">{businessName}</Text>
            <Text variant="bodySm" color="textSecondary" align="center">{link.label}</Text>
          </View>
        </PayLinkShare>

        <Card style={{ gap: spacing.sm }}>
          <SummaryCard>
            <SummaryRow label="Status" value={<StatusPill kind="openLink" status={openLinkStatus(link)} size="sm" />} />
            <SummaryRow label="Customers pay" value={openLinkAmountRule(link)} />
          </SummaryCard>
          <Text variant="bodyMed" weight="700">{openLinkReceived(link)}</Text>
        </Card>

        {suspended ? null : (
          <>
            <Button
              title={link.active ? 'Pause' : 'Resume'}
              variant="secondary"
              leftIcon={link.active ? 'pause' : 'play'}
              onPress={() => void setActive(!link.active)}
              loading={saving && form === null && !confirmingReplace}
              disabled={saving}
            />
            <Button title="Edit" variant="secondary" leftIcon="edit" onPress={openEdit} />
            <Button
              title="Replace link"
              variant="ghost"
              onPress={() => { setSheetError(undefined); setConfirmingReplace(true); }}
            />
          </>
        )}
        <Text variant="caption" color="textMuted" align="center">
          {`Print the poster from the web dashboard at ${WEB_DASHBOARD}`}
        </Text>

        <Text variant="bodyMed" weight="700">Latest payments</Text>
        {payments.length === 0 ? (
          <Text variant="bodySm" color="textSecondary">Nobody has paid through this link yet.</Text>
        ) : (
          payments.map((p) => {
            const purpose = openPaymentPurpose(p, link.label);
            const when = formatLinkTime(p.succeededAt ?? p.createdAt);
            return (
              <Card key={p.id} style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <View style={{ flex: 1, gap: spacing.xs }}>
                  <Text variant="bodyMed" weight="600" numberOfLines={1}>{purpose.title}</Text>
                  <Text variant="caption" color="textSecondary">{purpose.note ? `Customer's note · ${when}` : when}</Text>
                </View>
                <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
                  <Text variant="bodyMed" weight="700" numeric>{formatKobo(String(p.amount))}</Text>
                  <StatusPill kind="payLink" status={payLinkStatus(p)} size="sm" />
                </View>
              </Card>
            );
          })
        )}
      </ScrollView>

      <Sheet visible={form !== null} onClose={() => setForm(null)} title="Edit link" scrollable>
        {form ? (
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
            <Input
              label="Name"
              value={form.label}
              onChangeText={(label) => edit({ label })}
              maxLength={80}
              helper="Customers see this when they scan or open the link."
              error={fieldErrors.label}
            />
            <NairaField
              label="Lowest amount (optional)"
              value={form.min}
              onChangeText={(min) => edit({ min })}
              error={fieldErrors.min}
            />
            <NairaField
              label="Highest amount (optional)"
              value={form.max}
              onChangeText={(max) => edit({ max })}
              error={fieldErrors.max}
              helper="Leave both blank to take any amount within your TSA Pay limits."
            />
            <View style={{ gap: spacing.xs }}>
              <Text variant="bodySm" weight="600" color="textSecondary">Each payment expires after</Text>
              <SegmentedControl<string>
                segments={openLinkPaymentExpiryOptions(link.paymentExpiresIn ?? 0)}
                value={String(form.paymentExpiresIn)}
                onChange={(k) => edit({ paymentExpiresIn: Number(k) })}
              />
            </View>
            {sheetError ? <Text variant="bodySm" style={{ color: colors.danger }}>{sheetError}</Text> : null}
            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <View style={{ flex: 1 }}>
                <Button title="Cancel" variant="secondary" onPress={() => setForm(null)} />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Save" onPress={saveEdit} loading={saving} disabled={saving} />
              </View>
            </View>
          </View>
        ) : null}
      </Sheet>

      <Sheet visible={confirmingReplace} onClose={() => setConfirmingReplace(false)} title="Replace this link?">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
          <Text variant="bodySm" color="textSecondary">
            Your printed posters will stop working. Print the new one.
          </Text>
          {sheetError ? <Text variant="bodySm" style={{ color: colors.danger }}>{sheetError}</Text> : null}
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button title="Keep this one" variant="secondary" onPress={() => setConfirmingReplace(false)} />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                title="Replace link"
                accessibilityLabel="Confirm replace link"
                variant="danger"
                onPress={replace}
                loading={saving}
                disabled={saving}
              />
            </View>
          </View>
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
