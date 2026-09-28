// app/(dashboard)/booking-requests.tsx
//
// Booking requests (unified booking engine, Phase 2). Two views via a segmented
// control: "My requests" (buyer) shows the buyer's submitted requests + a
// Pay button once approved; "Incoming" (seller) shows requests for the
// merchant's services with Approve / Decline actions. Pay converts the request
// into an escrowed order via the standard escrow flow — or, for an MCGP
// request, a Spendable MCGP hold, which moves without a wallet signature and so
// is authorized with the app's PIN/biometric prompt first, as at cart checkout.
import React, { useCallback, useEffect, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { Badge, Button, Card, EmptyState, Spinner, Text, useToast } from '@/components/ui';
import {
  listBookingRequests,
  approveBookingRequest,
  rejectBookingRequest,
  type BookingRequest,
  type BookingRequestRow,
  type BookingRequestStatus,
} from '@/services/bookingRequestApi';
import { payBookingRequestAndEscrow } from '@/services/serviceCheckout';
import { useCurrency } from '@/contexts/CurrencyContext';
import { parseAmount, useMerchantPricing } from '@/hooks/useMerchantPricing';
import { LocalAmountInput, PricingCurrencySelect } from '@/components/products/MerchantPricingFields';
import AuthorizationModal from '@/components/common/AuthorizationModal';

type Tab = 'buyer' | 'seller';

// Map each status to a label + a semantic theme-color token (resolved against
// the active theme inside the component, so it tracks light/dark).
type ToneKey = 'warning' | 'success' | 'danger' | 'info' | 'textMuted';
const STATUS_TONE: Record<BookingRequestStatus, { label: string; tone: ToneKey }> = {
  requested: { label: 'Pending', tone: 'warning' },
  approved: { label: 'Approved', tone: 'success' },
  rejected: { label: 'Declined', tone: 'danger' },
  expired: { label: 'Expired', tone: 'textMuted' },
  converted: { label: 'Paid', tone: 'success' },
  cancelled: { label: 'Cancelled', tone: 'textMuted' },
};

export default function BookingRequestsScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [tab, setTab] = useState<Tab>('buyer');
  const [rows, setRows] = useState<BookingRequestRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  // An MCGP request waiting for the buyer to authorize its Spendable MCGP hold.
  const [authorizeFor, setAuthorizeFor] = useState<BookingRequest | null>(null);
  // Sellers quote in their own currency; the quote is sent in USD.
  const pricing = useMerchantPricing();
  const { formatPrice } = useCurrency();
  const quoteKey = (id: string) => `quote:${id}`;

  const load = useCallback(async () => {
    const res = await listBookingRequests(tab);
    setRows(res.success && res.data ? res.data.requests || [] : []);
    setLoading(false);
    setRefreshing(false);
  }, [tab]);

  useEffect(() => {
    setLoading(true);
    load();
  }, [load]);

  const onRefresh = () => {
    setRefreshing(true);
    load();
  };

  const withBusy = async (id: string, fn: () => Promise<void>) => {
    setBusyId(id);
    try {
      await fn();
    } finally {
      setBusyId(null);
    }
  };

  const handleApprove = (id: string) =>
    withBusy(id, async () => {
      const raw = pricing.amount(quoteKey(id));
      if (raw.trim() !== '') {
        const typed = parseAmount(raw);
        if (isNaN(typed) || typed <= 0) return toast.show('Enter a valid price', 'error');
        if (!pricing.ready) return toast.show('Exchange rates are still loading. Try again in a moment.', 'error');
      }
      const amount = pricing.usdFor(quoteKey(id));
      const res = await approveBookingRequest(id, amount);
      if (!res.success) return toast.show(res.message || 'Failed to approve', 'error');
      toast.show('Request approved', 'success');
      load();
    });

  const handleReject = (id: string) =>
    withBusy(id, async () => {
      const res = await rejectBookingRequest(id);
      if (!res.success) return toast.show(res.message || 'Failed to decline', 'error');
      toast.show('Request declined', 'success');
      load();
    });

  const isMcgp = (request: BookingRequest) => request.token?.toUpperCase() === 'MCGP';

  const runPay = (request: BookingRequest) =>
    withBusy(request.id, async () => {
      try {
        await payBookingRequestAndEscrow(request.id);
        toast.show(isMcgp(request) ? 'Booking paid from your Spendable MCGP' : 'Booking paid & funded via escrow', 'success');
        load();
      } catch (e: any) {
        toast.show(e.message || 'Payment failed', 'error');
      }
    });

  // Spendable MCGP moves with no wallet signature, so the buyer authorizes it
  // first; stablecoins are signed in the wallet and go straight through.
  const handlePay = (request: BookingRequest) => {
    if (isMcgp(request)) setAuthorizeFor(request);
    else runPay(request);
  };

  const Segment = ({ value, label }: { value: Tab; label: string }) => {
    const active = tab === value;
    return (
      <Button
        title={label}
        variant={active ? 'primary' : 'secondary'}
        size="sm"
        onPress={() => setTab(value)}
        style={{ flex: 1 }}
      />
    );
  };

  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flexDirection: 'row', gap: spacing.sm, padding: spacing.lg }}>
        <Segment value="buyer" label="My requests" />
        <Segment value="seller" label="Incoming" />
      </View>

      {loading ? (
        <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
          <Spinner label="Loading requests…" />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing['2xl'], gap: spacing.md }}
          refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
        >
          {tab === 'seller' && rows.some(({ request }) => request.status === 'requested') ? (
            <PricingCurrencySelect pricing={pricing} />
          ) : null}
          {rows.length === 0 ? (
            <EmptyState
              icon="list"
              title="No booking requests"
              subtitle={
                tab === 'buyer'
                  ? 'Requests you submit to providers will show here.'
                  : 'Booking requests for your services will show here.'
              }
            />
          ) : (
            rows.map(({ request, serviceName }) => {
              const tone = STATUS_TONE[request.status] ?? { label: request.status, tone: 'textMuted' as ToneKey };
              const toneColor = colors[tone.tone];
              const answers = request.answers || {};
              const answerKeys = Object.keys(answers);
              return (
                <Card key={request.id} padding="lg" style={{ gap: spacing.sm }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                    <Text variant="bodyMed" weight="700" style={{ flex: 1 }} numberOfLines={1}>
                      {serviceName || 'Service'}
                    </Text>
                    <Badge label={tone.label} background={toneColor} color={colors.onAccent} size="sm" />
                  </View>

                  {answerKeys.length > 0 ? (
                    <View style={{ gap: 2 }}>
                      {answerKeys.slice(0, 4).map((k) => (
                        <Text key={k} variant="caption" color="textSecondary">
                          {k}: {String(answers[k])}
                        </Text>
                      ))}
                    </View>
                  ) : null}

                  {request.status === 'rejected' && request.rejectReason ? (
                    <Text variant="caption" color="danger">
                      Reason: {request.rejectReason}
                    </Text>
                  ) : null}

                  {/* Buyer: pay once approved. Seller: approve/decline while pending. */}
                  {tab === 'buyer' && request.status === 'approved' ? (
                    <>
                      {request.quotedAmountUsd != null ? (
                        <Text variant="bodyMed" weight="700">
                          Quoted price: {formatPrice(request.quotedAmountUsd)}
                        </Text>
                      ) : null}
                      <Button
                        title="Pay & confirm"
                        leftIcon="shield"
                        onPress={() => handlePay(request)}
                        loading={busyId === request.id}
                        disabled={busyId === request.id}
                      />
                    </>
                  ) : null}
                  {tab === 'seller' && request.status === 'requested' ? (
                    <View style={{ gap: spacing.sm }}>
                      <LocalAmountInput
                        pricing={pricing}
                        amountKey={quoteKey(request.id)}
                        label="Price"
                        helper="Leave blank for a fixed-price listing"
                      />
                      <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                        <Button
                          title="Approve"
                          onPress={() => handleApprove(request.id)}
                          loading={busyId === request.id}
                          disabled={busyId === request.id}
                          style={{ flex: 1 }}
                        />
                        <Button
                          title="Decline"
                          variant="secondary"
                          onPress={() => handleReject(request.id)}
                          disabled={busyId === request.id}
                          style={{ flex: 1 }}
                        />
                      </View>
                    </View>
                  ) : null}
                </Card>
              );
            })
          )}
        </ScrollView>
      )}
      <AuthorizationModal
        visible={authorizeFor != null}
        title="Authorize payment"
        description={
          authorizeFor
            ? `Pay ${authorizeFor.quotedAmountUsd != null ? formatPrice(authorizeFor.quotedAmountUsd) + ' ' : ''}for this booking from your Spendable MCGP. It is held until you confirm the service.`
            : undefined
        }
        onAuthorized={() => {
          const request = authorizeFor;
          setAuthorizeFor(null);
          if (request) runPay(request);
        }}
        onCancel={() => setAuthorizeFor(null)}
      />
    </View>
  );
}
