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
import { ScrollView, View, Alert, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button,
  Card,
  CopyableField,
  Icon,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import { useCountdown } from '@/hooks/useCountdown';
import { DisputeSection } from '@/components/disputes/DisputeSection';
import {
  cancelOrder,
  getOrder,
  isAwaitingUser,
  isConvertedOrder,
  isTerminal,
  statusLabel,
  formatPromisedAmount,
  paycrestConversionHops,
  PaycrestError,
  type PaycrestOrder,
} from '@/services/paycrestApi';
import { getChainDisplay, type ChainKey } from '@/constants/chains';

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

/** requestedChain on a converted order can be anywhere LiFi routes to, not
 *  just one of the five chains above — fall back to the chain registry's own
 *  name rather than printing the raw key. */
const chainLabel = (key?: string): string => {
  if (!key) return '—';
  return CHAIN_LABELS[key] ?? getChainDisplay(key as ChainKey)?.name ?? key;
};

export default function PaycrestOrderScreen() {
  const { colors, spacing } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
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
  const converted = isConvertedOrder(order);
  const refunded = order.status === 'refunded';
  const refunding = order.status === 'refunding';
  const conversionHops = converted ? paycrestConversionHops(order.status, order.holder) : [];
  // Same shape of problem as `unmatched` — funds are stranded in a way no
  // retry fixes, and a human is already on it (see NeedsOperator on the
  // backend) — so it gets the same "needs explaining, not just labelling"
  // treatment rather than the default border/copy of an ordinary in-flight
  // order.
  const recoveryPending = order.status === 'recovery_pending';
  const needsOperator = unmatched || recoveryPending;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
        <Card
          style={{
            borderWidth: 1,
            borderColor: needsOperator
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
              color: needsOperator
                ? colors.warning
                : settled
                  ? colors.success
                  : colors.textPrimary,
            }}
          >
            {statusLabel(order)}
          </Text>

          {/* The two states that need explaining rather than labelling. The
              user did nothing wrong and cannot fix it themselves, so say both
              things plainly instead of showing a generic failure. */}
          {unmatched && (
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              Your transfer reached the provider but was never matched to this order. Your money is
              not lost — recovering it needs a person, and we have started that. Send us your bank
              receipt to speed it up.
            </Text>
          )}
          {recoveryPending && (
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              Your funds are stranded at the provider in a way we can&apos;t retry automatically. Nothing
              is lost — recovering it needs a person, and we have started that.
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

        {/* Converted orders route through our treasury before the user's
            wallet ever sees anything, which a plain status line does not
            convey — this timeline is what makes "why hasn't it arrived yet"
            visible instead of a black box. Reuses the same Hop/HopState
            rendering the stablecoin sell/buy progress screens already use
            (intent/[id].tsx), not a new stepper. */}
        {converted && (
          <Card>
            <Text
              variant="bodySm"
              weight="600"
              style={{ color: colors.textSecondary, marginBottom: spacing.sm }}
            >
              Progress
            </Text>
            <View style={{ gap: spacing.sm }}>
              {conversionHops.map((hop) => (
                <View
                  key={hop.key}
                  style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}
                >
                  {hop.state === 'done' ? (
                    <Icon name="checkCircle" size={20} color={colors.success} />
                  ) : hop.state === 'failed' ? (
                    <Icon name="closeCircle" size={20} color={colors.danger} />
                  ) : hop.state === 'active' ? (
                    <Spinner size="small" />
                  ) : (
                    <View
                      style={{
                        width: 18,
                        height: 18,
                        borderRadius: 9,
                        borderWidth: 2,
                        borderColor: colors.border,
                      }}
                    />
                  )}
                  <Text
                    variant="bodySm"
                    style={{ color: hop.state === 'pending' ? colors.textMuted : colors.textPrimary }}
                  >
                    {hop.label}
                  </Text>
                </View>
              ))}
            </View>
          </Card>
        )}

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
              {chainLabel(order.chain)}. Sending a different token or a different
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
          {converted ? (
            <>
              {/* order.tokenAmount/order.token are the SETTLEMENT leg — what
                  Paycrest itself filled, to our treasury — never what lands
                  in the user's wallet. Showing that here would be exactly
                  the silent substitution this phase exists to remove. A
                  refund reads as returned rather than delivered: the money
                  is going back to the SAME wallet, just never converted. */}
              <SummaryRow
                label={
                  refunded
                    ? 'Returned to your wallet'
                    : refunding
                      ? 'Returning to your wallet'
                      : 'You receive'
                }
                value={`${formatPromisedAmount(order) ?? order.tokenAmount ?? '—'} ${order.requestedToken ?? order.token}`}
                hint={refunded || refunding ? undefined : 'Guaranteed minimum — includes a conversion'}
              />
              <SummaryRow label="Network" value={chainLabel(order.requestedChain)} />
              <SummaryRow label="Route" value={`via ${order.token} on ${chainLabel(order.chain)}`} />
            </>
          ) : (
            <>
              <SummaryRow
                label={order.direction === 'buy' ? 'You receive' : 'You send'}
                value={`${order.tokenAmount ?? '—'} ${order.token}`}
              />
              <SummaryRow label="Network" value={chainLabel(order.chain)} />
            </>
          )}
          {order.rate ? <SummaryRow label="Rate" value={`₦${order.rate}`} /> : null}
          {order.settledAt ? <SummaryRow label="Completed" value={order.settledAt} /> : null}
          {/* Last, and deliberately: the reference means nothing to the user
              until something goes wrong, at which point support asks for it. */}
          <SummaryRow label="Reference" value={order.reference} />
        </SummaryCard>

        {order.txHash ? (
          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              {/* Best-effort label: on a converted order in/after a refund,
                  this is the only transaction-shaped field the API exposes
                  today, so it is read as the refund's — see Task 13's report
                  for the gap (no dedicated refund-tx field on the order yet). */}
              {converted && (refunded || refunding) ? 'Refund transaction' : 'Transaction'}
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
