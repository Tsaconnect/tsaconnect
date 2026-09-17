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
import { DepositProgress } from '@/components/paycrest/DepositProgress';
import { depositCardState } from '@/utils/paycrestDepositView';
import { DisputeSection } from '@/components/disputes/DisputeSection';
import {
  cancelOrder,
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
  // sentTx is set when this app just broadcast the deposit itself (sell.tsx →
  // goToOrder). Without it this screen cannot tell "the user still has to
  // send" from "we already sent, on their behalf, seconds ago".
  const { id, sentTx } = useLocalSearchParams<{ id: string; sentTx?: string }>();

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [addressShown, setAddressShown] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [cancelling, setCancelling] = useState(false);
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

  // Cancelling is the way out of the server's one-open-order lock: an
  // abandoned order otherwise blocks the next one for the full 30-minute
  // window. The confirm exists for the single question that matters — a user
  // who has already sent must not cancel, because the deposit is still on its
  // way to an order they would be marking dead.
  const confirmCancel = useCallback(() => {
    if (!order) return;
    Alert.alert(
      'Cancel this order?',
      order.direction === 'buy'
        ? 'Only cancel if you have NOT transferred the money yet. If you have already paid, do not cancel — contact support instead.'
        : 'Only cancel if you have NOT sent the crypto yet. If you have already sent it, do not cancel — contact support instead.',
      [
        { text: 'Keep it', style: 'cancel' },
        {
          text: 'Cancel order',
          style: 'destructive',
          onPress: async () => {
            setCancelling(true);
            try {
              const updated = await cancelOrder(order.id);
              setOrder(updated);
              // Nothing can move a cancelled order, so stop the poll rather
              // than letting it keep hitting the provider.
              stopped.current = true;
            } catch (e) {
              Alert.alert(
                'Could not cancel',
                e instanceof PaycrestError ? e.message : 'Please try again.',
              );
            } finally {
              setCancelling(false);
            }
          },
        },
      ],
    );
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

  const deposit = depositCardState({
    direction: order.direction,
    status: order.status,
    expired,
    sentTxHash: typeof sentTx === 'string' ? sentTx : undefined,
    paymentClaimedAt: order.paymentClaimedAt,
  });
  /** The deposit is on its way, so stop asking for it in the status line too. */
  const depositUnderway = deposit.kind === 'progress' || deposit.kind === 'claimed';

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
                : depositUnderway
                  ? // Instructing a user to send money they have already sent is
                    // how a sale becomes two. Once it is moving, report, do not ask.
                    `Confirming ${order.tokenAmount} ${order.token} from your wallet.`
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

        {deposit.kind === 'progress' && (
          <DepositProgress
            amountLabel={`${order.tokenAmount} ${order.token}`}
            chainLabel={CHAIN_LABELS[order.chain] ?? order.chain}
            txHash={deposit.txHash}
          />
        )}

        {/* The user says it is sent but this screen has no hash to show for it
            — a restart lost it, or they sent from a wallet we do not hold. Say
            we are waiting, and keep the address one tap away rather than gone:
            "I've sent it" is a button someone can press before they have. */}
        {deposit.kind === 'claimed' && (
          <Card>
            <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
              Waiting for your deposit to arrive
            </Text>
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              {order.tokenAmount} {order.token} on {CHAIN_LABELS[order.chain] ?? order.chain}. This
              confirms on its own once the provider sees it.
            </Text>
            {addressShown ? (
              <>
                <CopyableField value={order.depositAddress ?? ''} />
                <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
                  Only send again if nothing left your wallet — a second transfer will not be
                  matched to this order.
                </Text>
              </>
            ) : (
              <Button
                title="Show deposit address"
                variant="ghost"
                onPress={() => setAddressShown(true)}
                fullWidth
              />
            )}
          </Card>
        )}

        {deposit.kind === 'address' && (
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

        {/* Was a mailto. A dispute is the same intent with a record behind it:
            support can see it, it can be escalated to the provider, and the
            user can watch it move instead of wondering if the email landed.
            The mailto stays alongside for anyone who prefers email. */}
        <DisputeSection
          subjectType="paycrest_order"
          subjectId={order.id}
          disputable={!awaiting && order.status !== 'cancelled'}
          title={unmatched ? 'Open a dispute' : 'Report a problem'}
          prompt={
            unmatched
              ? 'Attach your bank receipt — it must show the session ID. That is what the provider needs to trace the payment.'
              : 'Tell us what went wrong. Attach a receipt or screenshot if you have one — it is the fastest way for us to chase this up.'
          }
        />

        {unmatched && <Button title="Email support" variant="ghost" onPress={contactSupport} fullWidth />}

        {/* Only while the order is still waiting on the user AND they have not
            claimed to have paid — past either point the server refuses, and
            offering a button that always fails is worse than not offering it. */}
        {awaiting && !order.paymentClaimedAt && (
          <Button
            title="Cancel order"
            variant="ghost"
            onPress={confirmCancel}
            loading={cancelling}
            disabled={cancelling}
            fullWidth
          />
        )}

        {error && (
          <Text variant="caption" style={{ color: colors.textMuted }}>
            {error} Retrying…
          </Text>
        )}
      </ScrollView>
    </SafeAreaView>
  );
}
