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
// twice, and a cancelled change keeps its hash for the refund or support
// (services/nameChangeHeld). What each state reads as is NameChangeStatusPanel.
import React, { useCallback, useRef, useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { TRANSPORT_ERROR } from '@/constants/api/config';
import { COMPANY } from '@/constants/legal/company';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { Button, ScreenHeader, Spinner, useToast } from '@/components/ui';
import { DiditWebView, requestCameraPermission } from '@/components/kyc/DiditSession';
import { StatusPanel } from '@/components/kyc/StatusPanel';
import { NameChangePayForm, paymentKey, type NameChangePayment } from '@/components/profile/NameChangePayForm';
import { NameChangeStatusPanel } from '@/components/profile/NameChangeStatusPanel';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { usePayAuthorization } from '@/hooks/usePayAuthorization';
import { makeIdempotencyKey } from '@/services/itApi';
import {
  NAME_CHANGE_CODES, cancelNameChange, getNameChange, isTerminalNameChange,
  startNameChange, startNameChangeVerification,
  type NameChange, type NameChangeOverview, type NameChangePay,
} from '@/services/nameChangeApi';
import { confirmPacer, reconcileHeldPayment, stillConfirming } from '@/services/nameChangeHeld';
import {
  confirmRecordedPayment, needsSupport, payNameChangeInStablecoin, shownFromChange, stablecoinShortfall,
  type PaymentPending,
} from '@/services/nameChangePay';
import { getSignableWallet } from '@/services/wallet';
import { goBack } from '@/utils/back';
import { nameChangeErrorMessage, refundMessage } from '@/utils/nameChange';
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

/**
 * One create request: the payment choice (method, coin, wallet), its
 * idempotency key and the body sent under it, and the change on screen when it
 * was made (`after`). A retry after a lost answer resends it exactly; a
 * different choice is a new request.
 */
type Attempt = { choice: string; key: string; body?: NameChangePay; after: string | null };

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
  const attempt = useRef<Attempt | null>(null);
  const acting = useRef(false);
  // How often the server is asked about the payment this phone holds (see services/nameChangeHeld).
  const [pacer] = useState(confirmPacer);

  const refresh = useCallback(async () => {
    try {
      const device = await getSignableWallet().catch(() => null);
      setWallet(device);
      let overview = await getNameChange(device);
      let c = overview.change;
      let held: PaymentPending = null;
      if (c?.payMethod === 'stablecoin') {
        const r = await reconcileHeldPayment(c, pacer.claim);
        held = r.held;
        if (r.taken) {
          overview = await getNameChange(device);
          c = overview.change;
        }
      }
      // A change other than the one on screen when the request was made — the
      // one a lost answer created, or any since — ends that request: resending
      // its key would only replay that change's creation.
      if (c && attempt.current && c.id !== attempt.current.after) attempt.current = null;
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
  }, [pacer]);

  useFocusEffect(useCallback(() => { void refresh(); }, [refresh]));

  const change = load.kind === 'ready' ? load.overview.change : null;
  const confirming = stillConfirming(change, pending);
  // Waiting on the server: an MCGP debit settling, a sent fee being proven, or
  // the verification's result. Each poll reads the change; a sent fee is
  // confirmed again only at its own pace.
  const waiting = !sessionUrl && !!change && (
    change.status === 'verifying' || (change.status === 'paying' && change.payMethod === 'mcgp_spendable') || confirming);
  useFocusPolling(refresh, POLL_MS, waiting);

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

  /** Send a stablecoin fee — the send itself asks the server about it once. */
  const sendFee = async (...args: Parameters<typeof payNameChangeInStablecoin>) => {
    try {
      await payNameChangeInStablecoin(...args);
    } finally {
      pacer.mark();
    }
  };

  const pay = (payment: NameChangePayment) => act(async () => {
    const payer = payment.payMethod === 'free' ? null : await deviceWallet();
    if (payer && payment.payMethod === 'stablecoin') {
      const short = await stablecoinShortfall(payment.option, payer);
      if (short) throw new Error(short);
    }
    const choice = `${paymentKey(payment)}:${payer?.toLowerCase() ?? ''}`;
    if (attempt.current?.choice !== choice) attempt.current = { choice, key: makeIdempotencyKey(), after: change?.id ?? null };
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
      started = await startNameChange(current.body, current.key, payer);
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
      await sendFee(started.change.id, started.transfer, payment.option, payer);
    }
  }, 'Could not pay the fee. Please try again.');

  /** Send the transfer of a change still to be paid — after a failed send, or a restart. */
  const payNow = (c: NameChange) => act(async () => {
    const shown = shownFromChange(c);
    if (!c.transfer || !shown) throw new Error('This payment can no longer be sent. Cancel the change and start again.');
    await sendFee(c.id, c.transfer, shown, await deviceWallet());
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

  // Asked for, so asked now — and the pacing restarts from here.
  const checkPayment = () => act(async () => {
    if (!change) return;
    pacer.mark();
    await confirmRecordedPayment(change.id);
  }, 'Could not check your payment. Please try again.');

  const cancel = () => {
    if (!change) return;
    // Money that left for a payment the server refused is not this change's to
    // return: never tell her nothing was charged.
    const back = needsSupport(pending)
      ? `The payment we couldn’t use isn’t returned by cancelling — email ${COMPANY.supportEmail} about it with its transaction.`
      : refundMessage(change);
    Alert.alert('Cancel name change?', `Your name stays as it is. ${back}`.trim(), [
      { text: 'Keep it', style: 'cancel' },
      {
        text: 'Cancel change',
        style: 'destructive',
        onPress: () => void act(async () => {
          await cancelNameChange();
          attempt.current = null;
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
        onRetry={() => void refresh()}
      />,
    );
  }

  return shell(
    <NameChangeStatusPanel
      change={change}
      overview={overview}
      pending={pending}
      confirming={confirming}
      wallet={wallet}
      busy={busy}
      error={error}
      onPayNow={() => request(() => { void payNow(change); })}
      onVerify={() => void verify()}
      onCheck={() => void checkPayment()}
      onCancel={cancel}
      onRefresh={() => void refresh()}
      onDone={() => goBack(PROFILE)}
      // A new change is a new request, whatever the last one was.
      onStartOver={() => { attempt.current = null; setStartOver(true); setError(undefined); }}
    />,
  );
}
