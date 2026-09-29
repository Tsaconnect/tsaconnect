// components/profile/NameChangeStatusPanel.tsx
//
// A name change as it stands, once there is one: what it is waiting on, the
// one thing she can do next, and — for a stablecoin fee this phone sent —
// where that payment stands. The screen (app/profile/name-change.tsx) owns the
// actions; this only says which apply.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { COMPANY } from '@/constants/legal/company';
import { Button, CopyableField, StatusPill, Text } from '@/components/ui';
import { StatusPanel } from '@/components/kyc/StatusPanel';
import { canCancelNameChange, type NameChange, type NameChangeOverview } from '@/services/nameChangeApi';
import { needsSupport, otherWalletMessage, type PaymentPending } from '@/services/nameChangePay';
import { DEPOSIT_OUTCOME_UNKNOWN } from '@/services/paycrestDeposit';
import { chainLabel, fmtAmount, fmtDate } from '@/utils/nameChange';

const sameAddress = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

/** Why the server will never use a payment this phone holds, for the support panel. */
const SUPPORT_REASON: Record<'refuted' | 'unmatched', string> = {
  refuted: 'The transaction didn’t pay this fee (it failed, or went from another wallet or to another address).',
  unmatched: 'We couldn’t match this transaction to a name change that can take it.',
};

export function NameChangeStatusPanel({
  change, overview, pending, confirming, wallet, busy, error,
  onPayNow, onVerify, onCheck, onCancel, onRefresh, onDone, onStartOver,
}: {
  change: NameChange;
  overview: NameChangeOverview;
  /** What this phone holds for the change's stablecoin fee. */
  pending: PaymentPending;
  /** A payment this phone sent is still being confirmed. */
  confirming: boolean;
  /** This phone's wallet. */
  wallet: string | null;
  busy: boolean;
  error?: string;
  onPayNow: () => void;
  onVerify: () => void;
  onCheck: () => void;
  onCancel: () => void;
  onRefresh: () => void;
  onDone: () => void;
  onStartOver: () => void;
}) {
  const { colors, spacing } = useTheme();

  const errorLine = error ? <Text key="err" variant="bodySm" align="center" style={{ color: colors.danger }}>{error}</Text> : null;
  const cancelButton = canCancelNameChange(change) && !confirming && pending?.kind !== 'unknown'
    ? <Button key="cancel" title="Cancel name change" variant="ghost" onPress={onCancel} disabled={busy} fullWidth />
    : null;
  const newChangeButton = overview.eligible && overview.quote
    ? <Button key="again" title="Change my name again" variant="secondary" onPress={onStartOver} fullWidth />
    : null;
  const done = <Button key="done" title="Done" variant="ghost" onPress={onDone} fullWidth />;
  const refreshButton = <Button key="refresh" title="Refresh" variant="secondary" onPress={onRefresh} fullWidth />;
  const supportFor = (txHash: string, lead = `If money left your wallet, email ${COMPANY.supportEmail} with this transaction:`) => (
    <View key="support" style={{ width: '100%', gap: spacing.sm }}>
      <Text variant="bodySm" color="textSecondary" align="center">{lead}</Text>
      <CopyableField label="Transaction" value={txHash} toastMessage="Transaction copied" />
    </View>
  );
  const panel = (p: Omit<React.ComponentProps<typeof StatusPanel>, 'children'>, ...nodes: React.ReactNode[]) => (
    <>
      <StatusPill kind="nameChange" status={change.status} style={{ alignSelf: 'center', marginBottom: spacing.md }} />
      <StatusPanel {...p}><View style={{ width: '100%', gap: spacing.md }}>{errorLine}{nodes}</View></StatusPanel>
    </>
  );
  const said = (fallback: string) => change.message || fallback;
  const attemptsLeft = Math.max(0, change.maxAttempts - change.attempts);

  switch (change.status) {
    case 'paying': {
      if (change.payMethod === 'mcgp_spendable') {
        // No cancel: the debit may be landing, and the server settles it first.
        return panel(
          { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Confirming your payment',
            subtitle: 'We’re taking the fee from your spendable MCGP. This takes a moment.' },
          refreshButton,
        );
      }
      if (confirming) {
        return panel(
          { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Payment sent — confirming',
            subtitle: 'Your payment is on its way. We confirm it as soon as the network has it — you don’t need to do anything.' },
          <Button key="check" title="Check now" variant="secondary" onPress={onCheck} loading={busy} disabled={busy} fullWidth />,
        );
      }
      if (pending?.kind === 'unknown') {
        return panel(
          { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Checking your payment', subtitle: DEPOSIT_OUTCOME_UNKNOWN },
          refreshButton,
        );
      }
      if (needsSupport(pending)) {
        return panel(
          { icon: 'closeCircle', iconColor: colors.danger, iconBg: colors.dangerBg, title: 'This payment couldn’t be used',
            subtitle: SUPPORT_REASON[pending.kind] },
          supportFor(pending.txHash),
          cancelButton,
        );
      }
      const from = change.transfer?.from;
      const wrongWallet = !!from && !sameAddress(from, wallet);
      return panel(
        { icon: 'wallet', iconColor: colors.accent, iconBg: colors.accentMuted, title: 'Finish paying the fee',
          subtitle: !change.transfer
            ? 'Your payment wasn’t sent. Cancel this change to choose how to pay again.'
            : wrongWallet
              ? otherWalletMessage(from)
              : `Send ${fmtAmount(change.payAmount)} ${change.payToken} on ${chainLabel(change.payChain)} to finish.` },
        change.transfer && !wrongWallet
          ? <Button key="pay" title="Pay now" onPress={onPayNow} loading={busy} disabled={busy} fullWidth />
          : null,
        cancelButton,
      );
    }
    case 'paid':
      return panel(
        { icon: 'shield', iconColor: colors.accent, iconBg: colors.accentMuted, title: 'Verify your identity',
          subtitle: 'Fee paid. Scan the ID your name should come from, then take a selfie. The name on the ID becomes your name.' },
        <Button key="verify" title="Verify your identity" onPress={onVerify} loading={busy} disabled={busy} fullWidth />,
        cancelButton,
      );
    case 'verifying':
      return panel(
        { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Checking your ID',
          subtitle: 'We’ll update your name as soon as your verification is approved. You stay verified meanwhile.' },
        refreshButton,
        done,
      );
    case 'declined':
      return panel(
        { icon: 'closeCircle', iconColor: colors.danger, iconBg: colors.dangerBg, title: 'Your ID wasn’t verified',
          subtitle: attemptsLeft > 0
            ? `You have ${attemptsLeft} ${attemptsLeft === 1 ? 'attempt' : 'attempts'} left, until ${fmtDate(change.expiresAt)}. Use a clear photo of a valid ID in good light.`
            : 'You have no attempts left.' },
        attemptsLeft > 0
          ? <Button key="retry" title="Try again" onPress={onVerify} loading={busy} disabled={busy} fullWidth />
          : null,
        done,
      );
    case 'held_for_review':
      return panel(
        { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Under review',
          subtitle: said(`Your ID shows ${change.newVerifiedName ?? 'a different name'}, which is quite different from ${change.oldName}. `
            + 'Our team is checking it’s you — we’ll let you know. Your name stays as it is until then.') },
        done,
      );
    case 'approved':
      return panel(
        { icon: 'checkCircle', iconColor: colors.success, iconBg: colors.successBg, title: 'Name changed',
          subtitle: `Your name is now ${change.newVerifiedName ?? 'the one on your ID'}.` },
        done,
      );
    case 'rejected':
      return panel(
        { icon: 'closeCircle', iconColor: colors.danger, iconBg: colors.dangerBg, title: 'Name change not approved',
          subtitle: said(`Your name stays ${change.oldName}.`) },
        newChangeButton,
        done,
      );
    case 'lapsed':
      return panel(
        { icon: 'clock', iconColor: colors.textMuted, iconBg: colors.surface, title: 'This name change expired',
          subtitle: said(`The verification attempts or the time for them ran out, so your name stays ${change.oldName}. A new change needs a new fee.`) },
        newChangeButton,
        done,
      );
    case 'cancelled':
      // A payment this phone sent for it: being confirmed (then refunded), refunding — or one only support can resolve.
      return panel(
        { icon: 'close', iconColor: colors.textMuted, iconBg: colors.surface, title: 'Name change cancelled',
          subtitle: confirming
            ? 'This name change closed before your payment was confirmed. We’re still confirming it, and it will be sent back to the wallet you paid from.'
            : needsSupport(pending)
              ? `${said(`Your name stays ${change.oldName}.`)} ${SUPPORT_REASON[pending.kind]}`
              : said(`Your name stays ${change.oldName}.`) },
        pending?.kind === 'sent'
          ? supportFor(pending.txHash, `If the refund hasn’t reached your wallet, email ${COMPANY.supportEmail} with this transaction:`)
          : needsSupport(pending) ? supportFor(pending.txHash) : null,
        newChangeButton,
        done,
      );
    default:
      return panel({ icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Name change' }, refreshButton);
  }
}
