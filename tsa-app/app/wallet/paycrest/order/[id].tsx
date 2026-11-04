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
  Stepper,
  SummaryCard,
  SummaryRow,
  Text,
  type IconName,
} from '@/components/ui';
import type { ColorTokens } from '@/theme';
import { useCountdown } from '@/hooks/useCountdown';
import { DepositProgress } from '@/components/paycrest/DepositProgress';
import { depositCardState } from '@/utils/paycrestDepositView';
import { DisputeSection } from '@/components/disputes/DisputeSection';
import { cancelRampOrder, getRampOrder } from '@/services/rampApi';
import {
  isAwaitingUser,
  isConvertedOrder,
  isDisputableOrder,
  isITConversionOrder,
  isTerminal,
  statusLabel,
  formatPromisedAmount,
  networkFeeText,
  paycrestConversionHops,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestStatus,
} from '@/services/paycrestApi';
import { DEPOSIT_OUTCOME_UNKNOWN } from '@/services/paycrestDeposit';
import { depositMayBeOut } from '@/services/depositGuard';
import { clearHold, depositHoldKey } from '@/services/payHold';
import { getChainDisplay, txExplorerUrl, type ChainKey } from '@/constants/chains';
import { formatDate } from '@/constants/orderStatus';
import { paycrestOrderSteps } from '@/utils/paycrestOrderSteps';
import { shortAddress } from '@/utils/sendForm';
import { formatNaira } from '@/utils/stableSellQuote';

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

const when = (iso: string) => formatDate(iso, true);

/** Statuses that prove the provider received her transfer. */
const TRANSFER_SEEN: PaycrestStatus[] = [
  'pending', 'settling', 'settled', 'converting', 'refunding', 'refunded', 'unmatched', 'recovery_pending',
];

type Tone = { icon: IconName; color: keyof ColorTokens; bg: keyof ColorTokens };
const TONES = {
  done: { icon: 'checkCircle', color: 'success', bg: 'successBg' },
  moving: { icon: 'clock', color: 'accent', bg: 'accentMuted' },
  attention: { icon: 'warning', color: 'warning', bg: 'warningBg' },
  ended: { icon: 'closeCircle', color: 'danger', bg: 'dangerBg' },
  returned: { icon: 'refresh', color: 'info', bg: 'infoBg' },
} satisfies Record<string, Tone>;

export default function PaycrestOrderScreen() {
  const { colors, spacing } = useTheme();
  // sentTx is set when this app just broadcast the deposit itself (sell.tsx →
  // goToOrder). Without it this screen cannot tell "the user still has to
  // send" from "we already sent, on their behalf, seconds ago".
  // unconfirmed is set when the send's outcome is unknown (its answer was lost,
  // or a relay may already be on-chain): waited on, never asked for again.
  const { id, sentTx, unconfirmed } = useLocalSearchParams<{ id: string; sentTx?: string; unconfirmed?: string }>();

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  // The deposit may already have left this phone (its hold, services/depositGuard).
  const [held, setHeld] = useState(false);
  const [addressShown, setAddressShown] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [cancelling, setCancelling] = useState(false);
  const stopped = useRef(false);

  const load = useCallback(async () => {
    if (!id) return;
    try {
      const o = await getRampOrder(id);
      const terminal = isTerminal(o.status);
      const holdKey = depositHoldKey(o.id);
      // Nothing more can happen to a terminal order's deposit: its hold has done its job.
      if (terminal) await clearHold(holdKey);
      // Read before the order is shown, so a held deposit is never asked for, even for a frame.
      setHeld(!terminal && o.direction === 'sell' && (await depositMayBeOut(holdKey, o.id)));
      setOrder(o);
      setError(null);
      // Stop polling the moment nothing upstream can change.
      if (terminal) stopped.current = true;
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
              const updated = await cancelRampOrder(order.id);
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

  // A purchase's conversion record is paid through that purchase: never
  // render pay-in instructions or Cancel for it, whatever its status reads.
  const conversionOnly = isITConversionOrder(order);
  const awaiting = isAwaitingUser(order.status) && !conversionOnly;
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

  /** This phone may have sent the deposit, with nothing yet to confirm it. */
  const mayBeOut = held || unconfirmed === '1';
  const deposit = depositCardState({
    direction: order.direction,
    status: order.status,
    expired,
    sentTxHash: typeof sentTx === 'string' ? sentTx : undefined,
    paymentClaimedAt: order.paymentClaimedAt,
    mayBeOut,
  });
  const tone = settled
    ? TONES.done
    : needsOperator
      ? TONES.attention
      : refunded || refunding
        ? TONES.returned
        : ['expired', 'failed', 'cancelled'].includes(order.status)
          ? TONES.ended
          : TONES.moving;
  // What this order is for, as she would put it: the naira a sale pays out, or the crypto a buy delivers.
  const headline =
    order.direction === 'sell'
      ? formatNaira(order.amount)
      : converted
        ? `${formatPromisedAmount(order) ?? order.tokenAmount ?? '—'} ${order.requestedToken ?? order.token}`
        : `${order.tokenAmount ?? '—'} ${order.token}`;
  const steps = converted
    ? conversionHops
    : paycrestOrderSteps({ order, sent: sentTx ? 'proven' : mayBeOut ? 'unconfirmed' : undefined, when });
  const transferSeen = TRANSFER_SEEN.includes(order.status);
  const explorerUrl = order.txHash && !converted ? txExplorerUrl(order.txHash, { chainKey: order.chain as ChainKey }) : undefined;

  /** The deposit is on its way, so stop asking for it in the status line too. */
  const depositUnderway = deposit.kind === 'progress' || deposit.kind === 'claimed' || deposit.kind === 'unconfirmed';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
        <Card style={{ alignItems: 'center', gap: spacing.xs, paddingVertical: spacing.lg }}>
          <View
            style={{
              width: 56,
              height: 56,
              borderRadius: 28,
              alignItems: 'center',
              justifyContent: 'center',
              backgroundColor: colors[tone.bg],
              marginBottom: spacing.xs,
            }}
          >
            <Icon name={tone.icon} size={30} color={colors[tone.color]} />
          </View>
          <Text variant="title2" weight="700" numeric align="center">
            {headline}
          </Text>
          {/* A converted buy's headline is what reaches her wallet, never
              order.tokenAmount (the SETTLEMENT leg Paycrest filled to our
              treasury) — and it is a floor, not a quote. A refund is labelled
              by the status line: the money goes back to the same wallet. */}
          {converted && !refunded && !refunding && (
            <Text variant="caption" color="textMuted" align="center">
              Guaranteed minimum — includes a conversion
            </Text>
          )}
          <Text variant="bodyMed" weight="600" align="center" style={{ color: colors[tone.color] }}>
            {statusLabel(order)}
          </Text>

          {/* The two states that need explaining rather than labelling. The
              user did nothing wrong and cannot fix it themselves, so say both
              things plainly instead of showing a generic failure. */}
          {unmatched && (
            <Text variant="bodySm" align="center" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              Your transfer reached the provider but was never matched to this order. Your money is
              not lost — recovering it needs a person, and we have started that. Send us your bank
              receipt to speed it up.
            </Text>
          )}
          {recoveryPending && (
            <Text variant="bodySm" align="center" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              Your funds are stranded at the provider in a way we can&apos;t retry automatically. Nothing
              is lost — recovering it needs a person, and we have started that.
            </Text>
          )}
          {awaiting && !expired && (
            <Text variant="bodySm" align="center" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              {order.direction === 'buy'
                ? order.paymentClaimedAt
                  ? // Same principle as the sell leg below: once she has told
                    // us she paid, re-asking her to transfer reads as if her
                    // tap did nothing. Report that we're checking instead.
                    `We're checking for your ₦${order.payAmount}. This usually takes under a minute.`
                  : `Transfer ₦${order.payAmount} to the account below. ${countdown} left.`
                : depositUnderway
                  ? // Instructing a user to send money they have already sent is
                    // how a sale becomes two. Once it is moving, report, do not ask.
                    `Confirming ${order.tokenAmount} ${order.token} from your wallet.`
                  : `Send ${order.tokenAmount} ${order.token} from your wallet. ${countdown} left.`}
            </Text>
          )}
          {conversionOnly && isAwaitingUser(order.status) && (
            <Text variant="bodySm" align="center" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              This conversion starts once your purchase is paid.
            </Text>
          )}
          {order.failureReason && !unmatched && (
            <Text variant="caption" align="center" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
              {order.failureReason}
            </Text>
          )}
        </Card>

        {/* Where the money is, step by step — the answer to "why hasn't it
            arrived yet". A converted order routes through our treasury, so it
            has its own hops (paycrestConversionHops); a direct one walks
            opened → sent → received → paying → paid. */}
        <Card>
          <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.md }}>
            Progress
          </Text>
          <Stepper steps={steps} />
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

        {/* The deposit may have left this phone, but nothing confirms it yet —
            a lost answer, or an app kill after the broadcast. No address and
            no way to cancel: sending again could pay the order twice. */}
        {deposit.kind === 'unconfirmed' && (
          <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
            <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
              Your deposit may already be on its way
            </Text>
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              {DEPOSIT_OUTCOME_UNKNOWN} This order updates on its own once the provider sees it.
            </Text>
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
              {chainLabel(order.chain)}. Sending a different token or a different
              network will not reach the provider.
            </Text>
          </Card>
        )}

        <Card>
          <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.xs }}>
            Details
          </Text>
          <SummaryCard>
            {/* What she receives is the headline above. A buy's naira figure is
                left out while the transfer instruction is up — that card states
                it, and is the authoritative one — and shown only once the
                provider has seen the transfer: an expired or cancelled buy was
                never paid, and payAmount is only what she was asked for. */}
            {order.direction === 'buy' && transferSeen && order.payAmount ? (
              <SummaryRow label="You paid" value={formatNaira(order.payAmount)} />
            ) : null}
            {order.direction === 'sell' ? (
              <SummaryRow label="You send" value={`${order.tokenAmount ?? '—'} ${order.token}`} />
            ) : null}
            {converted ? (
              <>
                <SummaryRow label="Network" value={chainLabel(order.requestedChain)} />
                <SummaryRow label="Route" value={`via ${order.token} on ${chainLabel(order.chain)}`} />
              </>
            ) : (
              <SummaryRow label="Network" value={chainLabel(order.chain)} />
            )}
            {order.direction === 'buy' && networkFeeText(order) ? (
              <SummaryRow
                label="Network fee"
                value={networkFeeText(order)!}
                tip="The provider's cost to deliver on-chain, taken out of what you receive. On larger buys our fee covers it instead."
              />
            ) : null}
            {order.rate ? <SummaryRow label="Rate" value={`${formatNaira(order.rate)} per ${order.token}`} /> : null}
            <SummaryRow label="Opened" value={when(order.createdAt)} />
            {order.settledAt ? <SummaryRow label="Completed" value={when(order.settledAt)} /> : null}
            {/* Last, and deliberately: the reference means nothing to the user
                until something goes wrong, at which point support asks for it. */}
            <SummaryRow label="Reference" value={order.reference} />
          </SummaryCard>
        </Card>

        {order.txHash ? (
          <Card>
            <Text variant="overline" color="textMuted">
              {/* Best-effort label: on a converted order in/after a refund,
                  this is the only transaction-shaped field the API exposes
                  today, so it is read as the refund's — see Task 13's report
                  for the gap (no dedicated refund-tx field on the order yet). */}
              {converted && (refunded || refunding) ? 'Refund transaction' : 'Transaction'}
            </Text>
            <CopyableField value={order.txHash} display={shortAddress(order.txHash)} toastMessage="Transaction hash copied" />
            {/* Only where the chain is certain: a converted order's hash may be on either leg. */}
            {explorerUrl ? (
              <Button
                title="View on explorer"
                variant="ghost"
                size="sm"
                rightIcon="arrowRight"
                onPress={() => Linking.openURL(explorerUrl).catch(() => undefined)}
              />
            ) : null}
          </Card>
        ) : null}

        {/* Was a mailto. A dispute is the same intent with a record behind it:
            support can see it, it can be escalated to the provider, and the
            user can watch it move instead of wondering if the email landed.
            The mailto stays alongside for anyone who prefers email. */}
        <DisputeSection
          subjectType="paycrest_order"
          subjectId={order.id}
          disputable={isDisputableOrder(order.status)}
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
            offering a button that always fails is worse than not offering it.
            Nor once this phone sent the deposit, or may have. */}
        {awaiting && !order.paymentClaimedAt && !mayBeOut && !sentTx && (
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
