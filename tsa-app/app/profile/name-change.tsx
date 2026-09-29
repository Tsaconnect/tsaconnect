// app/profile/name-change.tsx
//
// A verified user's name change: her name is the one on her ID, so she pays
// the re-verification fee, verifies again with the ID she wants her name taken
// from, and the document's name becomes her name — at once when it clearly is
// the same person's name, after a review when it isn't. She stays verified the
// whole time. Explainer and payment (NameChangePayForm) → password + PIN/
// biometric → pay (from the account's proven wallet; one never proven is
// proven through the wallet-link flow, by createApiRequest, first) → "Verify
// your identity" (the same Didit session as first-time KYC) → the outcome. The name
// change notifications, and Didit's `?flow=name_change` callback, open here.
//
// A stablecoin fee is sent from this phone and confirmed by hash
// (services/nameChangePay). What this phone sent is recorded until the server
// has used it: it is confirmed again while the chain catches up — even after the
// change closed, when the server takes it late and refunds it — never paid
// twice, and a cancelled change keeps its hash for the refund or support.
import React, { useCallback, useRef, useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { TRANSPORT_ERROR } from '@/constants/api/config';
import { COMPANY } from '@/constants/legal/company';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { Button, CopyableField, ScreenHeader, Spinner, StatusPill, Text, useToast } from '@/components/ui';
import { DiditWebView, requestCameraPermission } from '@/components/kyc/DiditSession';
import { StatusPanel } from '@/components/kyc/StatusPanel';
import { NameChangePayForm, paymentKey, type NameChangePayment } from '@/components/profile/NameChangePayForm';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { usePayAuthorization } from '@/hooks/usePayAuthorization';
import { makeIdempotencyKey } from '@/services/itApi';
import {
  NAME_CHANGE_CODES, canCancelNameChange, cancelNameChange, getNameChange, isTerminalNameChange,
  startNameChange, startNameChangeVerification,
  type NameChange, type NameChangeOverview, type NameChangePay,
} from '@/services/nameChangeApi';
import {
  confirmRecordedPayment, forgetNameChangePayment, otherWalletMessage,
  payNameChangeInStablecoin, paymentPending, shownFromChange, stablecoinShortfall,
  type PaymentPending,
} from '@/services/nameChangePay';
import { DEPOSIT_OUTCOME_UNKNOWN } from '@/services/paycrestDeposit';
import { getSignableWallet } from '@/services/wallet';
import { goBack } from '@/utils/back';
import { chainLabel, fmtAmount, fmtDate, nameChangeErrorMessage, refundMessage } from '@/utils/nameChange';
import { errorCode } from '@/utils/payCheckout';

type Load =
  | { kind: 'loading' }
  | { kind: 'unavailable'; message: string }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; overview: NameChangeOverview };

const POLL_MS = 5_000;
const PROFILE = '/profile' satisfies Href;

const UNAVAILABLE: Record<NonNullable<NameChangeOverview['unavailableReason']>, string> = {
  disabled: 'Name changes aren’t available yet.',
  not_verified: 'Your identity isn’t verified yet, so you can edit your name on your profile.',
};

const sameAddress = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

/**
 * What this phone holds for a stablecoin change's fee. `late`: a closed
 * change's payment was just taken (and is being refunded) — the change is worth
 * re-reading. Only a change that used the payment lets its hash go: a cancelled
 * one keeps it, the reference for its refund or for support.
 */
async function reconcileHeldPayment(c: NameChange): Promise<{ held: PaymentPending; late: boolean }> {
  const held = await paymentPending(c.id);
  if (c.status === 'paying' || held?.kind === 'refuted') return { held, late: false };
  if (c.status === 'cancelled') {
    if (held?.kind !== 'sent' || c.feeStatus !== 'none') return { held, late: false };
    // It closed before our payment was proven: the server still takes it (and refunds it).
    const taken = await confirmRecordedPayment(c.id).catch(() => null);
    return { held: await paymentPending(c.id), late: !!taken };
  }
  await forgetNameChangePayment(c.id);
  return { held: null, late: false };
}

/** A stablecoin payment this phone sent that the server has not proven yet: it is confirmed again, never resent. */
const stillConfirming = (c: NameChange | null, held: PaymentPending): boolean =>
  !!c && c.payMethod === 'stablecoin' && held?.kind === 'sent'
  && (c.status === 'paying' || (c.status === 'cancelled' && c.feeStatus === 'none'));

export default function NameChangeScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [load, setLoad] = useState<Load>({ kind: 'loading' });
  const [wallet, setWallet] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [password, setPassword] = useState('');
  const [sessionUrl, setSessionUrl] = useState<string | null>(null);
  const [pending, setPending] = useState<PaymentPending>(null);
  // She chose to start again after a change that ended.
  const [startOver, setStartOver] = useState(false);
  // One request per payment choice (method, coin, wallet): a retry after a
  // lost answer resends it exactly — same key, same body — and a different
  // choice is a new request with a new key.
  const attempt = useRef<{ choice: string; key: string; body?: NameChangePay } | null>(null);
  const acting = useRef(false);

  const refresh = useCallback(async () => {
    try {
      const device = await getSignableWallet().catch(() => null);
      setWallet(device);
      let overview = await getNameChange(device);
      let c = overview.change;
      let held: PaymentPending = null;
      if (c?.payMethod === 'stablecoin') {
        const r = await reconcileHeldPayment(c);
        held = r.held;
        if (r.late) {
          overview = await getNameChange(device);
          c = overview.change;
        }
      }
      setPending(held);
      // With no change to show, a reason none can start is all there is to say.
      if (!c && overview.unavailableReason) {
        setLoad({ kind: 'unavailable', message: UNAVAILABLE[overview.unavailableReason] });
        return;
      }
      setLoad({ kind: 'ready', overview });
    } catch (e) {
      const code = errorCode(e);
      if (code === NAME_CHANGE_CODES.DISABLED) setLoad({ kind: 'unavailable', message: UNAVAILABLE.disabled });
      else setLoad((prev) => (prev.kind === 'ready' ? prev : { kind: 'error', message: nameChangeErrorMessage(e, 'Could not load your name change.') }));
    }
  }, []);

  useFocusEffect(useCallback(() => { void refresh(); }, [refresh]));

  const change = load.kind === 'ready' ? load.overview.change : null;
  const confirming = stillConfirming(change, pending);
  // Waiting on the server: an MCGP debit settling, a sent fee being proven, or the verification's result.
  const waiting = !sessionUrl && !!change && (
    change.status === 'verifying' || (change.status === 'paying' && change.payMethod === 'mcgp_spendable') || confirming);
  const poll = useCallback(async () => {
    // A closed change's late payment is confirmed by refresh itself.
    if (confirming && change?.status === 'paying') await confirmRecordedPayment(change.id).catch(() => null);
    await refresh();
  }, [confirming, change, refresh]);
  useFocusPolling(poll, POLL_MS, waiting);

  /** Run one action at a time; a refusal is shown, and the change re-read. */
  const act = async (fn: () => Promise<void>, fallback: string) => {
    if (acting.current) return;
    acting.current = true;
    setBusy(true);
    setError(undefined);
    try {
      await fn();
    } catch (e) {
      setError(nameChangeErrorMessage(e, fallback));
    } finally {
      acting.current = false;
      setBusy(false);
      await refresh();
    }
  };

  /** This phone's wallet, re-read for the action: the account's linked wallet, and the one a stablecoin fee is sent from. */
  const deviceWallet = async () => {
    const w = await getSignableWallet().catch(() => null);
    if (!w) throw new Error('Import your wallet on this phone: the fee is paid from it.');
    return w;
  };

  const pay = (payment: NameChangePayment) => act(async () => {
    const payer = payment.payMethod === 'free' ? null : await deviceWallet();
    if (payer && payment.payMethod === 'stablecoin') {
      const short = await stablecoinShortfall(payment.option, payer);
      if (short) throw new Error(short);
    }
    const choice = `${paymentKey(payment)}:${payer?.toLowerCase() ?? ''}`;
    if (attempt.current?.choice !== choice) attempt.current = { choice, key: makeIdempotencyKey() };
    const current = attempt.current;
    if (!current.body) {
      current.body = !payer || payment.payMethod === 'free'
        ? { payMethod: 'free', password }
        : payment.payMethod === 'stablecoin'
          ? { payMethod: 'stablecoin', chain: payment.option.chain, token: payment.option.token, password }
          : { payMethod: 'mcgp_spendable', password };
    }
    let started;
    try {
      started = await startNameChange(current.body, current.key);
    } catch (e) {
      // Only an answered refusal ends this request; a lost answer is retried as it was.
      if (errorCode(e) !== TRANSPORT_ERROR) attempt.current = null;
      throw e;
    }
    attempt.current = null;
    setPassword('');
    setStartOver(false);
    if (payment.payMethod === 'stablecoin' && payer) {
      if (!started.transfer) throw new Error('The server did not say where to send the fee. Please try again.');
      await payNameChangeInStablecoin(started.change.id, started.transfer, payment.option, payer);
    }
  }, 'Could not pay the fee. Please try again.');

  /** Send the transfer of a change still to be paid — after a failed send, or a restart. */
  const payNow = (c: NameChange) => act(async () => {
    const shown = shownFromChange(c);
    if (!c.transfer || !shown) throw new Error('This payment can no longer be sent. Cancel the change and start again.');
    await payNameChangeInStablecoin(c.id, c.transfer, shown, await deviceWallet());
  }, 'Could not send your payment.');

  const { request, authorization } = usePayAuthorization<() => void>({
    title: () => 'Authorize paying the name change fee',
    onAuthorized: (run) => run(),
  });

  const onPay = (p: NameChangePayment) => {
    if (!password) { setError('Enter your account password to confirm.'); return; }
    setError(undefined);
    request(() => { void pay(p); });
  };

  const verify = async () => {
    if (!(await requestCameraPermission())) return;
    await act(async () => {
      const session = await startNameChangeVerification();
      setSessionUrl(session.url);
    }, 'Could not start verification. Please try again.');
  };

  const checkPayment = () => act(async () => {
    if (change) await confirmRecordedPayment(change.id);
  }, 'Could not check your payment. Please try again.');

  const cancel = () => {
    if (!change) return;
    Alert.alert('Cancel name change?', `Your name stays as it is. ${refundMessage(change)}`.trim(), [
      { text: 'Keep it', style: 'cancel' },
      {
        text: 'Cancel change',
        style: 'destructive',
        onPress: () => void act(async () => {
          await cancelNameChange();
          toast.success('Name change cancelled');
        }, 'Could not cancel. Please try again.'),
      },
    ]);
  };

  if (sessionUrl) {
    return <DiditWebView url={sessionUrl} onFinished={() => { setSessionUrl(null); void refresh(); }} />;
  }

  const shell = (children: React.ReactNode) => (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Change name" fallbackRoute={PROFILE} />
      <ScrollView contentContainerStyle={{ flexGrow: 1, padding: spacing.lg, gap: spacing.md }} keyboardShouldPersistTaps="handled">
        {children}
      </ScrollView>
      <AuthorizationModal {...authorization} />
    </SafeAreaView>
  );

  if (load.kind === 'loading') {
    return shell(<View style={{ flex: 1, justifyContent: 'center' }}><Spinner size="large" /></View>);
  }
  if (load.kind === 'unavailable' || load.kind === 'error') {
    return shell(
      <StatusPanel
        icon={load.kind === 'error' ? 'closeCircle' : 'user'}
        iconColor={load.kind === 'error' ? colors.danger : colors.accent}
        iconBg={load.kind === 'error' ? colors.dangerBg : colors.accentMuted}
        title={load.kind === 'error' ? 'Could not load your name change' : 'Change name'}
        subtitle={load.message}
      >
        <View style={{ width: '100%', gap: spacing.md }}>
          {load.kind === 'error' ? <Button title="Try again" onPress={() => void refresh()} fullWidth /> : null}
          <Button title="Go back" variant="ghost" onPress={() => goBack(PROFILE)} fullWidth />
        </View>
      </StatusPanel>,
    );
  }

  const { overview } = load;
  if (!change || (isTerminalNameChange(change.status) && startOver)) {
    return shell(
      <NameChangePayForm
        overview={overview}
        busy={busy}
        error={error}
        walletAddress={wallet}
        password={password}
        onChangePassword={setPassword}
        onPay={onPay}
      />,
    );
  }

  const errorLine = error ? <Text key="err" variant="bodySm" align="center" style={{ color: colors.danger }}>{error}</Text> : null;
  const cancelButton = canCancelNameChange(change) && !confirming && pending?.kind !== 'unknown'
    ? <Button key="cancel" title="Cancel name change" variant="ghost" onPress={cancel} disabled={busy} fullWidth />
    : null;
  const newChangeButton = overview.eligible && overview.quote
    ? <Button key="again" title="Change my name again" variant="secondary" onPress={() => { setStartOver(true); setError(undefined); }} fullWidth />
    : null;
  const done = <Button key="done" title="Done" variant="ghost" onPress={() => goBack(PROFILE)} fullWidth />;
  const refreshButton = <Button key="refresh" title="Refresh" variant="secondary" onPress={() => void refresh()} fullWidth />;
  const supportFor = (txHash: string, lead = `If money left your wallet, email ${COMPANY.supportEmail} with this transaction:`) => (
    <View key="support" style={{ width: '100%', gap: spacing.sm }}>
      <Text variant="bodySm" color="textSecondary" align="center">{lead}</Text>
      <CopyableField label="Transaction" value={txHash} toastMessage="Transaction copied" />
    </View>
  );
  const pill = <StatusPill kind="nameChange" status={change.status} style={{ alignSelf: 'center', marginBottom: spacing.md }} />;
  const panel = (p: Omit<React.ComponentProps<typeof StatusPanel>, 'children'>, ...nodes: React.ReactNode[]) =>
    shell(<>{pill}<StatusPanel {...p}><View style={{ width: '100%', gap: spacing.md }}>{errorLine}{nodes}</View></StatusPanel></>);
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
          <Button key="check" title="Check now" variant="secondary" onPress={() => void checkPayment()} loading={busy} disabled={busy} fullWidth />,
        );
      }
      if (pending?.kind === 'unknown') {
        return panel(
          { icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Checking your payment', subtitle: DEPOSIT_OUTCOME_UNKNOWN },
          refreshButton,
        );
      }
      if (pending?.kind === 'refuted') {
        return panel(
          { icon: 'closeCircle', iconColor: colors.danger, iconBg: colors.dangerBg, title: 'This payment couldn’t be used',
            subtitle: 'The transaction didn’t pay this fee (it failed, or went from another wallet or to another address).' },
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
          ? <Button key="pay" title="Pay now" onPress={() => request(() => { void payNow(change); })} loading={busy} disabled={busy} fullWidth />
          : null,
        cancelButton,
      );
    }
    case 'paid':
      return panel(
        { icon: 'shield', iconColor: colors.accent, iconBg: colors.accentMuted, title: 'Verify your identity',
          subtitle: 'Fee paid. Scan the ID your name should come from, then take a selfie. The name on the ID becomes your name.' },
        <Button key="verify" title="Verify your identity" onPress={() => void verify()} loading={busy} disabled={busy} fullWidth />,
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
          ? <Button key="retry" title="Try again" onPress={() => void verify()} loading={busy} disabled={busy} fullWidth />
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
      // A payment this phone sent for it: being confirmed (then refunded), or refunding — or refuted.
      return panel(
        { icon: 'close', iconColor: colors.textMuted, iconBg: colors.surface, title: 'Name change cancelled',
          subtitle: confirming
            ? 'This name change closed before your payment was confirmed. We’re still confirming it, and it will be sent back to the wallet you paid from.'
            : said(`Your name stays ${change.oldName}.`) },
        pending?.kind === 'sent'
          ? supportFor(pending.txHash, `If the refund hasn’t reached your wallet, email ${COMPANY.supportEmail} with this transaction:`)
          : pending?.kind === 'refuted' ? supportFor(pending.txHash) : null,
        newChangeButton,
        done,
      );
    default:
      return panel({ icon: 'clock', iconColor: colors.warning, iconBg: colors.warningBg, title: 'Name change' }, refreshButton);
  }
}
