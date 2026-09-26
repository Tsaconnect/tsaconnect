// app/merchants/pay-links/[id].tsx
//
// One payment link, as its merchant sees it: what it is for, who it is for,
// where it stands, and — while nobody has started paying it — the QR code,
// share and copy to send it again, and Cancel link. Cancelling asks first: the
// link stops working and that can't be undone. Refreshed every 15 s while it
// can still move.
import React, { useCallback, useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button, Card, ErrorState, Sheet, Spinner, StatusPill, SummaryCard, SummaryRow, Text, useToast,
} from '@/components/ui';
import { PayLinkShare, TestLinkBanner } from '@/components/pay/PayLinkShare';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import {
  cancelPayLink, getPayLink, OWNER_CODES, payLinkModeOf, payLinkModeParam, type PayLink, type PayLinkMode,
} from '@/services/payMerchantApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';
import { formatLinkTime, payLinkReceives, payLinkStatus, payLinkTimeRow } from '@/utils/payLinks';
import { formatKobo } from '@/utils/stableSellQuote';

const POLL_MS = 15_000;

export default function PayLinkDetail() {
  const { id, mode: modeParam } = useLocalSearchParams<{ id: string; mode?: string }>();
  const { load, reload } = useOwnPayMerchant();
  if (load.kind === 'loading') {
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }
  if (load.kind === 'error') {
    return <ErrorState title="Could not load your TSA Pay account" subtitle={load.message} onRetry={reload} />;
  }
  if (load.kind === 'none') {
    return <ErrorState title="No TSA Pay account" subtitle="Apply to accept payments with TSA Pay first." />;
  }
  // The list opens a link in its own mode; opened cold, a link is in the mode she is in.
  const mode: PayLinkMode = payLinkModeParam(modeParam) ?? payLinkModeOf(load.merchant);
  return <LinkDetail id={String(id)} mode={mode} businessName={load.merchant.businessName} />;
}

function LinkDetail({ id, mode, businessName }: { id: string; mode: PayLinkMode; businessName: string }) {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [link, setLink] = useState<PayLink | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [confirming, setConfirming] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string | undefined>();
  // A double tap on Cancel link must send one cancel.
  const submitting = useRef(false);

  const refresh = useCallback(async () => {
    try {
      setLink(await getPayLink(mode, id));
      setError(undefined);
    } catch (e) {
      setError(errorMessage(e, 'Could not load this payment link.'));
    }
  }, [id, mode]);

  // Paid, failed, expired or cancelled, it will not move again.
  const settled = link !== null && link.status !== 'pending' && link.status !== 'processing';
  useFocusPolling(refresh, POLL_MS, !settled);

  const cancel = async () => {
    if (submitting.current) return;
    submitting.current = true;
    setCancelling(true);
    setCancelError(undefined);
    try {
      setLink(await cancelPayLink(mode, id));
      setConfirming(false);
      toast.success('Link cancelled');
    } catch (e) {
      const message = errorMessage(e, 'Could not cancel the link. Please try again.');
      if (errorCode(e) === OWNER_CODES.NOT_CANCELLABLE) {
        // Someone has started paying it (or it closed): there is nothing left
        // to confirm — close the sheet and show the link as it now is.
        setConfirming(false);
        toast.error(message);
        void refresh();
      } else {
        setCancelError(message);
      }
    } finally {
      submitting.current = false;
      setCancelling(false);
    }
  };

  if (link === null) {
    if (error) return <ErrorState title="Could not load this payment link" subtitle={error} onRetry={refresh} />;
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }
  const open = link.status === 'pending' && !link.cancelled;
  const timeRow = payLinkTimeRow(link);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        {link.mode === 'test' ? <TestLinkBanner /> : null}
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.md }}>
          <Text variant="title3" weight="700" numeric>{formatKobo(String(link.amount))}</Text>
          <StatusPill kind="payLink" status={payLinkStatus(link)} />
        </View>
        {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}

        <Card>
          <SummaryCard>
            <SummaryRow label="For" value={link.description} />
            {link.customerName ? <SummaryRow label="Customer" value={link.customerName} /> : null}
            {payLinkReceives(link) ? (
              <SummaryRow label="You'll receive" value={formatKobo(String(link.merchantNet))} />
            ) : null}
            <SummaryRow label="Reference" value={link.reference} />
            <SummaryRow label="Created" value={formatLinkTime(link.createdAt)} />
            {timeRow ? <SummaryRow label={timeRow.label} value={formatLinkTime(timeRow.at)} /> : null}
          </SummaryCard>
        </Card>

        {open ? (
          <>
            <PayLinkShare link={link} businessName={businessName} />
            <Button title="Cancel link" variant="ghost" onPress={() => { setCancelError(undefined); setConfirming(true); }} />
          </>
        ) : null}
      </ScrollView>

      <Sheet visible={confirming} onClose={() => setConfirming(false)} title="Cancel this link?">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
          <Text variant="bodySm" color="textSecondary">
            {"The link will stop working. This can't be undone."}
          </Text>
          {cancelError ? <Text variant="bodySm" style={{ color: colors.danger }}>{cancelError}</Text> : null}
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button title="Keep link" variant="secondary" onPress={() => setConfirming(false)} />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                title="Cancel link"
                accessibilityLabel="Confirm cancel link"
                variant="danger"
                onPress={cancel}
                loading={cancelling}
                disabled={cancelling}
              />
            </View>
          </View>
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
