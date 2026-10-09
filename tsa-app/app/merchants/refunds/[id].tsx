// app/merchants/refunds/[id].tsx
//
// One refund, as the business owner sends it. Asked for on the web (and pushed to her to
// approve), it waits here: the network and token to send it as (the customer's own first),
// her balance of it and of gas, and Send refund. Sending is allowed only from the wallet the
// server names (her proven TSA wallet) — any other wallet on this device is refused before
// anything is prepared — and only after she authorizes (AuthorizationModal), which names
// the amount, token, network and the customer's wallet. The signed transfer goes to the
// refund's own submit route; then the screen polls until the transfer is confirmed or fails.
// While it waits she can cancel it instead.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Linking, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button, Card, EmptyState, ErrorState, RadioGroup, Sheet, StatusPill, SummaryCard, SummaryRow, Text, useToast,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { ScreenSpinner } from '@/components/pay/OwnPayMerchantGate';
import { MAINNET_CHAINS, txExplorerUrl, type ChainKey } from '@/constants/chains';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import { usePayAuthorization } from '@/hooks/usePayAuthorization';
import { cancelPayRefund, getPayRefund, type PayRefundDetail, type PayRefundOption } from '@/services/payMerchantApi';
import { sendPayRefund } from '@/services/payRefundSend';
import { getSignableWallet, nativeBalance } from '@/services/wallet';
import { getWalletBalances, linkActiveWallet, type WalletBalance } from '@/services/walletApi';
import { errorMessage } from '@/utils/payCheckout';
// A refund id is a server UUID like a payment id: the same gate keeps a crafted deep link out of the request path.
import { isPaymentId as isRefundId } from '@/utils/paymentId';
import {
  defaultOption, optionKey, refundFunding, refundStatusLine, sameAddress, unprovenWalletMessage, wrongWalletMessage, type RefundFunding,
} from '@/utils/payRefunds';
import { formatNativeAmount, hasGasShortfall, shortAddress } from '@/utils/sendForm';
import { formatKobo } from '@/utils/stableSellQuote';

export default function RefundDetailScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  if (!isRefundId(id)) return <EmptyState title="Refund not found" subtitle="This refund link isn't valid." />;
  return <RefundDetail id={id} />;
}

/** The option's mainnet chain, only when its key and id agree (gas is read on the network the transfer is signed for). */
function optionChain(o: PayRefundOption | undefined) {
  const chain = o ? MAINNET_CHAINS[o.chainKey as ChainKey] : undefined;
  return chain && chain.chainId === o?.chainId ? chain : undefined;
}

function RefundDetail({ id }: { id: string }) {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [detail, setDetail] = useState<PayRefundDetail | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [choice, setChoice] = useState<string | undefined>();
  // undefined while unknown; null when this device holds no signing key.
  const [signer, setSigner] = useState<string | null | undefined>(undefined);
  const [balances, setBalances] = useState<WalletBalance[] | null>(null);
  const [gas, setGas] = useState<number | null>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | undefined>();
  const [confirmingCancel, setConfirmingCancel] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string | undefined>();
  const [proving, setProving] = useState(false);
  const [proveError, setProveError] = useState<string | undefined>();
  // One send, cancel or wallet proof at a time: a double tap must not sign twice.
  const busy = useRef(false);
  // Only the latest read counts: a poll set off before a send landed must not undo it.
  const seq = useRef(0);

  // The device's wallet is read with the refund every time: she may have switched wallets
  // in Manage Wallet and come back.
  const refresh = useCallback(async () => {
    const mine = ++seq.current;
    try {
      const [got, wallet] = await Promise.all([getPayRefund(id), getSignableWallet().catch(() => null)]);
      if (mine !== seq.current) return;
      setDetail(got);
      setSigner(wallet);
      setError(undefined);
    } catch (e) {
      if (mine !== seq.current) return;
      setError(errorMessage(e, 'Could not load this refund.'));
    }
  }, [id]);

  const status = detail?.refund.status;
  // Waiting for her, or on its way: it can still move. Anything else is final.
  const moving = !detail || status === 'requested' || status === 'submitted';
  useFocusPolling(refresh, status === 'requested' ? 15_000 : 5_000, moving);

  const option = detail ? detail.options.find((o) => optionKey(o) === choice) ?? defaultOption(detail.options) : undefined;
  const chain = optionChain(option);
  const chainId = option?.chainId;
  const chainKey = option?.chainKey;
  const fromAddress = detail?.fromAddress;

  // Her balance of the token and of gas on the chosen network — mainnet: refunds are live money.
  useEffect(() => {
    if (!chainId || !chainKey || !fromAddress || status !== 'requested') return undefined;
    let live = true;
    setBalances(null);
    setGas(null);
    getWalletBalances(chainId, fromAddress, 'mainnet')
      .then((r) => { if (live) setBalances(r.success && r.data ? r.data : null); })
      .catch(() => undefined);
    if (chain) {
      nativeBalance(chainKey as ChainKey, fromAddress, 'mainnet')
        .then((wei) => { if (live) setGas(wei === null ? null : Number(wei) / 1e18); })
        .catch(() => undefined);
    }
    return () => { live = false; };
  }, [chainId, chainKey, chain, fromAddress, status]);

  const send = async (o: PayRefundOption) => {
    if (busy.current || !detail) return;
    busy.current = true;
    setSending(true);
    setSendError(undefined);
    try {
      // The device's own spelling of the address the server names: the signing key is stored under it.
      const from = signer && sameAddress(signer, detail.fromAddress) ? signer : detail.fromAddress;
      const done = await sendPayRefund(id, o, detail.refund.toAddress, from);
      seq.current += 1;
      setDetail((d) => (d ? { ...d, refund: done } : d));
      toast.success('Refund sent. Waiting for the network to confirm it.');
    } catch (e) {
      setSendError(errorMessage(e, 'Could not send the refund. Please try again.'));
      // The server may have recorded it before its answer was lost: show what it holds.
      // Send is offered again only while it still says requested.
      void refresh();
    } finally {
      busy.current = false;
      setSending(false);
    }
  };

  // The server names no wallet until she proves the one on this device (plans review I10):
  // the app's own wallet proof (linkActiveWallet, as apiRequest uses on WALLET_PROOF_REQUIRED),
  // then the refund is read again and names it.
  const prove = async () => {
    if (busy.current) return;
    busy.current = true;
    setProving(true);
    setProveError(undefined);
    try {
      const linked = await linkActiveWallet();
      if (!linked.success) {
        setProveError(linked.message || 'Could not prove this wallet. Please try again.');
        return;
      }
      await refresh();
    } catch (e) {
      setProveError(errorMessage(e, 'Could not prove this wallet. Please try again.'));
    } finally {
      busy.current = false;
      setProving(false);
    }
  };

  const who = detail?.payerFirstName || 'Your customer';
  const { request: authorize, authorization } = usePayAuthorization<PayRefundOption>({
    title: () => 'Authorize refund',
    description: (o) => (o && detail
      ? `Send ${o.amountDisplay} ${o.tokenSymbol} on ${o.chainName} to ${who} (${shortAddress(detail.refund.toAddress)}).`
      : undefined),
    onAuthorized: (o) => { void send(o); },
  });

  const cancel = async () => {
    if (busy.current) return;
    busy.current = true;
    setCancelling(true);
    setCancelError(undefined);
    try {
      const cancelled = await cancelPayRefund(id);
      seq.current += 1;
      setDetail((d) => (d ? { ...d, refund: cancelled } : d));
      setConfirmingCancel(false);
      toast.success('Refund request cancelled');
    } catch (e) {
      const message = errorMessage(e, 'Could not cancel the request. Please try again.');
      if ((e as { status?: number } | null)?.status === 409) {
        // Sent (or cancelled) meanwhile: nothing left to confirm — show it as it now is.
        setConfirmingCancel(false);
        toast.error(message);
        void refresh();
      } else {
        setCancelError(message);
      }
    } finally {
      busy.current = false;
      setCancelling(false);
    }
  };

  if (!detail) {
    if (error) return <ErrorState title="Could not load this refund" subtitle={error} onRetry={refresh} />;
    return <ScreenSpinner />;
  }

  const { refund, payment } = detail;
  const funding: RefundFunding = option ? refundFunding(balances, option) : { kind: 'unknown' };
  const nativeSymbol = chain?.nativeCurrency.symbol ?? '';
  const noGas = hasGasShortfall(gas, null, 0, false);
  const unproven = detail.fromAddress === '';
  const wrongWallet = !unproven && signer !== undefined && !sameAddress(signer, detail.fromAddress);
  const canSend = status === 'requested' && !!option && !unproven && signer !== undefined && !wrongWallet
    && funding.kind !== 'empty' && funding.kind !== 'short' && !noGas && !sending;
  const txUrl = refund.txHash && refund.chainId ? txExplorerUrl(refund.txHash, { chainId: refund.chainId }) : undefined;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.md }}>
          <Text variant="title3" weight="700" numeric>{`${refund.amount} USD`}</Text>
          <StatusPill kind="payRefund" status={refund.status} />
        </View>
        {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}

        <Card>
          <SummaryCard>
            <SummaryRow label="To" value={who} />
            <SummaryRow label="Customer wallet" value={shortAddress(refund.toAddress)} />
            {status === 'requested' && option ? (
              <SummaryRow label="Sending" value={`${option.amountDisplay} ${option.tokenSymbol} on ${option.chainName}`} />
            ) : null}
            <SummaryRow label="For" value={payment.description || payment.merchantName} />
            {payment.code ? <SummaryRow label="Payment" value={payment.code} /> : null}
            <SummaryRow label="They paid" value={formatKobo(String(payment.customerTotal))} />
            {refund.reason ? <SummaryRow label="Reason" value={refund.reason} /> : null}
          </SummaryCard>
        </Card>

        {status === 'requested' && option ? (
          <>
            {unproven ? (
              <Card style={{ gap: spacing.sm }}>
                <Text variant="bodySm">{unprovenWalletMessage}</Text>
                {proveError ? <Text variant="bodySm" style={{ color: colors.danger }}>{proveError}</Text> : null}
                <Button title="Prove this wallet" variant="secondary" onPress={prove} loading={proving} disabled={proving} />
              </Card>
            ) : null}
            {wrongWallet ? (
              <Card>
                <Text variant="bodySm" style={{ color: colors.danger }}>{wrongWalletMessage(detail.fromAddress)}</Text>
              </Card>
            ) : null}
            {detail.options.length > 1 ? (
              <View style={{ gap: spacing.sm }}>
                <Text variant="bodyMed" weight="700">Send it as</Text>
                <RadioGroup
                  options={detail.options.map((o) => ({
                    value: optionKey(o),
                    label: `${o.tokenSymbol} on ${o.chainName}`,
                    sublabel: `${o.amountDisplay} ${o.tokenSymbol}${o.isDefault ? ' · how they paid' : ''}`,
                  }))}
                  value={optionKey(option)}
                  onChange={setChoice}
                />
              </View>
            ) : null}
            <FundingNote funding={funding} option={option} />
            {noGas ? (
              <Text variant="bodySm" style={{ color: colors.danger }}>
                {`You have no ${nativeSymbol} on ${option.chainName} for the network fee. Add a little ${nativeSymbol} to your TSA wallet first.`}
              </Text>
            ) : gas !== null ? (
              <Text variant="caption" color="textMuted">{`Network fee paid in ${nativeSymbol} · you have ${formatNativeAmount(gas)} ${nativeSymbol}`}</Text>
            ) : null}
            {sendError ? <Text variant="bodySm" style={{ color: colors.danger }}>{sendError}</Text> : null}
            <Button title="Send refund" size="lg" onPress={() => authorize(option)} loading={sending} disabled={!canSend} />
          </>
        ) : (
          <Text variant="bodySm" color="textSecondary">{refundStatusLine(detail)}</Text>
        )}
        {/* Withdrawable whenever it still waits — even when no network can carry it (plans review P12). */}
        {status === 'requested' ? (
          <Button
            title="Cancel request"
            variant="ghost"
            onPress={() => { setCancelError(undefined); setConfirmingCancel(true); }}
            disabled={sending}
          />
        ) : null}
        {txUrl ? <Button title="View on explorer" variant="secondary" onPress={() => void Linking.openURL(txUrl)} /> : null}
      </ScrollView>

      <Sheet visible={confirmingCancel} onClose={() => setConfirmingCancel(false)} title="Cancel this refund request?">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
          <Text variant="bodySm" color="textSecondary">
            Nothing has been sent. You can ask for a refund again from your dashboard on the web.
          </Text>
          {cancelError ? <Text variant="bodySm" style={{ color: colors.danger }}>{cancelError}</Text> : null}
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button title="Keep it" variant="secondary" onPress={() => setConfirmingCancel(false)} />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                title="Cancel request"
                accessibilityLabel="Confirm cancel request"
                variant="danger"
                onPress={cancel}
                loading={cancelling}
                disabled={cancelling}
              />
            </View>
          </View>
        </View>
      </Sheet>
      <AuthorizationModal {...authorization} />
    </SafeAreaView>
  );
}

/** Her balance of the token; with none (or too little), how to get some. */
function FundingNote({ funding, option }: { funding: RefundFunding; option: PayRefundOption }) {
  const { spacing } = useTheme();
  const sym = option.tokenSymbol;
  if (funding.kind === 'enough') {
    return <Text variant="caption" color="textMuted">{`You have ${funding.have} ${sym} on ${option.chainName}`}</Text>;
  }
  if (funding.kind !== 'empty' && funding.kind !== 'short') return null;
  const shortBy = funding.kind === 'short' ? `You have ${funding.have} ${sym}; this refund needs ${option.amountDisplay}. ` : '';
  const receive = `/wallet/receive?symbol=${encodeURIComponent(sym)}&chainKey=${encodeURIComponent(option.chainKey)}`;
  const buy = `/wallet/paycrest/buy?asset=${encodeURIComponent(sym)}`;
  return (
    <Card style={{ gap: spacing.sm }}>
      <Text variant="bodyMed" weight="700">
        {funding.kind === 'empty' ? `No ${sym} on ${option.chainName}` : `Not enough ${sym} on ${option.chainName}`}
      </Text>
      <Text variant="bodySm" color="textSecondary">
        {`${shortBy}Refunds are paid from your TSA wallet in USDT or USDC. Receive ${sym} from an exchange or another wallet, or buy it in the app, then come back.`}
      </Text>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <View style={{ flex: 1 }}>
          <Button title="Receive" variant="secondary" onPress={() => router.push(receive as never)} />
        </View>
        <View style={{ flex: 1 }}>
          <Button title="Buy" variant="secondary" onPress={() => router.push(buy as never)} />
        </View>
      </View>
    </Card>
  );
}
