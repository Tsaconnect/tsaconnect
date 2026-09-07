// app/wallet/paycrest/order/[id].tsx
//
// One Paycrest ramp order, from "we're waiting for your transfer" to done.
//
// There is no webhook endpoint yet, so nothing pushes progress to the app —
// this screen polls, and stops as soon as the order reaches a state that
// cannot change on its own.
//
// The state this screen exists to handle properly is `unmatched`: the user
// paid in full, inside the window, the bank confirmed it, and the protocol
// never registered the money. It is not a failure the user caused and not one
// they can retry, and there is no automatic refund — so it must never be
// rendered as an ordinary "expired" or "failed". It gets its own copy and a
// direct route to support.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ScrollView, Alert, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button,
  Card,
  CopyableField,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import { useCountdown } from '@/hooks/useCountdown';
import {
  getOrder,
  isAwaitingUser,
  isTerminal,
  statusLabel,
  PaycrestError,
  type PaycrestOrder,
} from '@/services/paycrestApi';

/** Delivery landed in ~73s end to end in live testing, so a 5s poll shows the
 *  result within a tick or two of it actually happening without hammering. */
const POLL_MS = 5000;

const SUPPORT_EMAIL = 'support@tsaconnect.com';

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

export default function PaycrestOrderScreen() {
  const { colors, spacing } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const stopped = useRef(false);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const o = await getOrder(id);
      setOrder(o);
      setError(null);
      // Stop polling the moment nothing upstream can change.
      if (isTerminal(o.status)) stopped.current = true;
    } catch (e) {
      // Keep whatever we already have on screen — a dropped poll must not
      // blank out payment instructions the user is mid-transfer against.
      setError(e instanceof PaycrestError ? e.message : 'Could not refresh this order.');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    stopped.current = false;
    load();
    const timer = setInterval(() => {
      if (stopped.current) return;
      load();
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  const { label: countdown, expired } = useCountdown(
    order && isAwaitingUser(order.status) ? order.validUntil : undefined,
  );

  const contactSupport = useCallback(() => {
    if (!order) return;
    const subject = `Payment not credited — order ${order.reference}`;
    const body = [
      'My payment was not credited to this order.',
      '',
      `Order reference: ${order.reference}`,
      `Amount: ${order.currency} ${order.payAmount ?? order.amount}`,
      `Paid to: ${order.payAccountName ?? ''} · ${order.payAccountNumber ?? ''} · ${order.payInstitution ?? ''}`,
      `Created: ${order.createdAt}`,
      '',
      'Please attach your bank receipt (it must show the session ID).',
    ].join('\n');
    Linking.openURL(
      `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`,
    ).catch(() => Alert.alert('Could not open mail', `Please email ${SUPPORT_EMAIL}.`));
  }, [order]);

  if (loading && !order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
        <Spinner />
      </SafeAreaView>
    );
  }
  if (!order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
        <Card style={{ margin: spacing.md }}>
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error ?? 'Order not found.'}
          </Text>
        </Card>
      </SafeAreaView>
    );
  }

  const awaiting = isAwaitingUser(order.status);
  const unmatched = order.status === 'unmatched';
  const settled = order.status === 'settled';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
        <Card
          style={{
            borderWidth: 1,
            borderColor: unmatched
              ? colors.warning
              : settled
                ? colors.success
                : colors.border,
          }}
        >
          <Text
            variant="title3"
            weight="700"
            style={{
              color: unmatched
                ? colors.warning
                : settled
                  ? colors.success
                  : colors.textPrimary,
            }}
          >
            {statusLabel(order)}
          </Text>

          {/* The one state that needs explaining rather than labelling. The
              user did nothing wrong and cannot fix it themselves, so say both
              things plainly instead of showing a generic failure. */}
          {unmatched && (
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              Your transfer reached the provider but was never matched to this order. Your money is
              not lost — recovering it needs a person, and we have started that. Send us your bank
              receipt to speed it up.
            </Text>
          )}
          {awaiting && !expired && (
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              {order.direction === 'buy'
                ? `Transfer ₦${order.payAmount} to the account below. ${countdown} left.`
                : `Send ${order.tokenAmount} ${order.token} from your wallet. ${countdown} left.`}
            </Text>
          )}
          {order.failureReason && !unmatched && (
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
              {order.failureReason}
            </Text>
          )}
        </Card>

        {/* Payment instructions stay visible while the order is still waiting
            on the user — this screen is where they land after tapping "I've
            sent the money", and they may not have actually sent it yet. */}
        {awaiting && order.direction === 'buy' && !expired && (
          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Pay into
            </Text>
            <CopyableField label="Bank" value={order.payInstitution ?? ''} />
            <CopyableField label="Account number" value={order.payAccountNumber ?? ''} />
            <CopyableField label="Account name" value={order.payAccountName ?? ''} />
            {/* Copyable value stays bare — the user pastes it into a bank
                app, where a currency symbol would be rejected — but the label
                carries the currency so the row is not the only unlabelled
                money figure on the screen. */}
            <CopyableField label="Amount (₦)" value={order.payAmount ?? ''} />
          </Card>
        )}

        {awaiting && order.direction === 'sell' && !expired && (
          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Send from your wallet to
            </Text>
            <CopyableField value={order.depositAddress ?? ''} />
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              Send exactly {order.tokenAmount} {order.token} on{' '}
              {CHAIN_LABELS[order.chain] ?? order.chain}. Sending a different token or a different
              network will not reach the provider.
            </Text>
          </Card>
        )}

        <SummaryCard>
          {/* On a buy the transfer instruction above already states what to
              send, and it is the authoritative figure — repeating it here as
              "You pay" was redundant at best. Before the sender fee moved into
              the rate the two even disagreed: "you pay 800" beside "transfer
              804.02". Only the sell direction needs this row, where the fiat
              figure is what the user RECEIVES and appears nowhere else. */}
          {order.direction === 'sell' ? (
            <SummaryRow label="You receive" value={`₦${order.amount}`} />
          ) : null}
          <SummaryRow
            label={order.direction === 'buy' ? 'You receive' : 'You send'}
            value={`${order.tokenAmount ?? '—'} ${order.token}`}
          />
          {order.rate ? <SummaryRow label="Rate" value={`₦${order.rate}`} /> : null}
          <SummaryRow label="Network" value={CHAIN_LABELS[order.chain] ?? order.chain} />
          {order.settledAt ? <SummaryRow label="Completed" value={order.settledAt} /> : null}
          {/* Last, and deliberately: the reference means nothing to the user
              until something goes wrong, at which point support asks for it. */}
          <SummaryRow label="Reference" value={order.reference} />
        </SummaryCard>

        {order.txHash ? (
          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Transaction
            </Text>
            <CopyableField value={order.txHash} />
          </Card>
        ) : null}

        {unmatched && <Button title="Get help" onPress={contactSupport} fullWidth />}

        {error && (
          <Text variant="caption" style={{ color: colors.textMuted }}>
            {error} Retrying…
          </Text>
        )}
      </ScrollView>
    </SafeAreaView>
  );
}
