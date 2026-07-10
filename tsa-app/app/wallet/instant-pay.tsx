// app/wallet/instant-pay.tsx
//
// Instant Pay — send crypto to another TSA Connect user by @username. Resolves
// the username to a wallet address, then runs a review → authorize →
// prepare → sign → submit (confirm) flow. Restyled onto the TSA Signature
// design system (@/theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: this is a visual/structural restyle ONLY. Every piece of
// money handling is preserved verbatim — the debounced username resolution
// (resolveUsername), amount + balance validation, chain/token selection (incl.
// the per-token supported-chains auto-correct), gas estimation, and the
// prepareSendTransaction → signTransaction → submitTransaction confirm flow.
// No recipient, amount, or submit logic was altered.
import React, { useState, useEffect, useCallback } from 'react';
import {
  View, ScrollView, KeyboardAvoidingView, Platform, FlatList, Linking,
} from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { signTransaction, getProvider, getActiveWallet } from '../../services/wallet';
import api from '../../components/services/api';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  submitTransaction,
  resolveUsername,
  type WalletBalanceWithChain,
  type ResolvedUser,
} from '../../services/walletApi';
import AuthorizationModal from '../../components/common/AuthorizationModal';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Avatar,
  Button,
  Card,
  Divider,
  Icon,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';

type BalanceEntry = WalletBalanceWithChain;

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'pending' | 'failure';

// Round token-symbol badge. Color is sourced from the token registry's
// iconColor (data-driven, like wallet/home & wallet/send) and falls back to a
// neutral token instead of a hardcoded hex map.
const TokenBadge = ({
  symbol,
  size = 36,
  bg,
}: {
  symbol: string;
  size?: number;
  bg: string;
}) => (
  <View
    style={{
      width: size,
      height: size,
      borderRadius: size / 2,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: bg,
    }}
  >
    <Text variant="bodyMed" weight="700" style={{ fontSize: size * 0.38, color: '#FFFFFF' }}>
      {symbol[0]}
    </Text>
  </View>
);

const InstantPay = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokens, tokenList } = useTokens();

  // Token-color map derived from the registry (matches wallet/home & send), so
  // badge colors stay data-driven instead of hardcoded hex.
  const tokenColorFor = useCallback(
    (symbol: string): string => tokens[symbol]?.iconColor || colors.textMuted,
    [tokens, colors.textMuted],
  );

  const [username, setUsername] = useState('');
  const [amount, setAmount] = useState('');
  const [selectedToken, setSelectedToken] = useState('USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [balances, setBalances] = useState<BalanceEntry[]>([]);
  const [resolvedUser, setResolvedUser] = useState<ResolvedUser | null>(null);
  const [resolving, setResolving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);
  const [copied, setCopied] = useState(false);
  // Sends leave the ACTIVE device wallet; @username payments arrive at the
  // REGISTERED wallet. When they differ, say so instead of letting the user
  // assume one wallet does both.
  const [activeWallet, setActiveWallet] = useState('');
  const [registeredWallet, setRegisteredWallet] = useState('');
  useEffect(() => {
    getActiveWallet().then((a) => setActiveWallet(a || ''));
    api
      .getProfile()
      .then((r: any) => setRegisteredWallet(r?.data?.walletAddress || ''))
      .catch(() => {});
  }, []);
  const walletMismatch =
    !!activeWallet && !!registeredWallet && activeWallet.toLowerCase() !== registeredWallet.toLowerCase();
  const shortAddr = (a: string) => `${a.slice(0, 6)}…${a.slice(-4)}`;

  const getSupportedChainsForToken = useCallback((symbol: string): ChainKey[] => {
    const registryChains = tokens[symbol]?.chains ?? [];
    if (registryChains.length > 0) {
      return registryChains;
    }

    // EVM-only flow: instant pay runs on EVM chains, so chainKey narrows safely.
    const inferredChains = Array.from(
      new Set(
        balances
          .filter((entry) => entry.symbol === symbol)
          .map((entry) => entry.chainKey as ChainKey)
      )
    );
    return inferredChains;
  }, [balances, tokens]);

  const loadBalances = useCallback(async () => {
    try {
      setLoading(true);
      const chainResults = await Promise.all(
        CHAIN_KEYS.map(async (chainKey) => {
          const result = await getWalletBalances(CHAINS[chainKey].chainId);
          return { chainKey, result };
        })
      );

      const allBalances: BalanceEntry[] = [];
      const seen = new Set<string>();

      for (const { chainKey, result } of chainResults) {
        if (!result.success || !result.data) continue;
        for (const entry of normalizeWalletBalances(result.data, chainKey)) {
          allBalances.push(entry);
          seen.add(`${entry.symbol}-${entry.chainKey}`);
        }
      }

      for (const t of tokenList) {
        for (const chain of t.chains) {
          const key = `${t.symbol}-${chain}`;
          if (!seen.has(key)) {
            allBalances.push({
              symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00',
              contractAddress: '', decimals: t.decimals,
              chainKey: chain as ChainKey, chainName: CHAINS[chain as ChainKey]?.shortName || chain,
            });
          }
        }
      }

      setBalances(allBalances);
    } catch (err) {
      console.error('InstantPay: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList]);

  useFocusEffect(useCallback(() => { loadBalances(); }, [loadBalances]));

  useEffect(() => {
    const chains = getSupportedChainsForToken(selectedToken);
    if (chains.length === 0) return;
    if (!chains.includes(selectedChain)) {
      setSelectedChain(chains[0]);
    }
  }, [getSupportedChainsForToken, selectedChain, selectedToken]);

  const bal = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain)?.balance || '0';
  const activeChain = CHAINS[selectedChain];

  // Debounced username lookup
  useEffect(() => {
    if (username.trim().length < 3) { setResolvedUser(null); return; }
    const t = setTimeout(async () => {
      setResolving(true); setError('');
      try {
        const r = await resolveUsername(username.trim());
        if (r.success && r.data) setResolvedUser(r.data);
        else { setResolvedUser(null); setError(r.message || 'User not found'); }
      } catch { setResolvedUser(null); setError('Failed to look up user'); }
      finally { setResolving(false); }
    }, 600);
    return () => clearTimeout(t);
  }, [username]);

  const validate = (): string | null => {
    if (!username.trim()) return 'Enter a username.';
    if (!resolvedUser) return 'User not found.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    if (Number(amount) > Number(bal)) return 'Insufficient balance.';
    return null;
  };

  const handleReview = async () => {
    const e = validate(); if (e) { setError(e); return; }
    setError('');
    try {
      setLoading(true);
      const from = await getActiveWallet();
      if (!from) throw new Error('No wallet found. Set up your wallet first.');
      const r = await prepareSendTransaction(selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId, { fromAddress: from });
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      const gp = BigInt(r.data.gasPrice || '0');
      const gl = BigInt(r.data.gasLimit || '21000');
      setGasEstimate((Number(gp * gl) / 1e18).toFixed(6));
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSendRequest = () => {
    if (!resolvedUser) return;
    setShowAuthModal(true);
  };

  const handleSend = async () => {
    setShowAuthModal(false);
    setScreen('sending'); setError('');
    try {
      // ONE captured address through prepare → sign → submit: a mid-flow
      // wallet switch must fail the signature pin, not pair another
      // account's nonce with this signature (queued-forever tx).
      const from = await getActiveWallet();
      if (!from) throw new Error('No wallet found. Set up your wallet first.');
      const r = await prepareSendTransaction(selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId, { fromAddress: from });
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare');
      const signed = await signTransaction({
        type: 0,
        to: r.data.to, data: r.data.data, value: r.data.value,
        gasLimit: r.data.gasLimit, gasPrice: r.data.gasPrice,
        nonce: r.data.nonce, chainId: r.data.chainId,
      }, from);
      const res = await submitTransaction(signed, 'send', selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId, from);
      if (!res.success || !res.data?.txHash) throw new Error(res.message || 'Failed');
      setTxHash(res.data.txHash);

      // "Sent" must mean MINED, not merely broadcast — an accepted tx can sit
      // in the mempool forever (nonce queue, underpriced gas) and the recipient
      // never receives it. Wait bounded; on timeout show the honest "pending"
      // state instead of claiming success.
      try {
        const receipt = await getProvider(selectedChain).waitForTransaction(res.data.txHash, 1, 90_000);
        if (receipt && receipt.status === 0) {
          throw new Error('The transfer reverted on-chain. No funds were taken beyond gas.');
        }
        setScreen(receipt ? 'success' : 'pending');
      } catch (waitErr: any) {
        if (/reverted on-chain/.test(waitErr?.message || '')) throw waitErr;
        // Timeout / RPC read failure — the tx may still confirm; don't call it failed.
        setScreen('pending');
      }
    } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
  };

  // ── Result ──
  if (screen === 'success' || screen === 'pending' || screen === 'failure') {
    const ok = screen === 'success';
    const pending = screen === 'pending';
    const explorerUrl = txHash && activeChain?.explorerUrl ? `${activeChain.explorerUrl}/tx/${txHash}` : null;
    const iconBg = ok ? colors.successBg : pending ? colors.accentMuted : colors.dangerBg;
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
        {/* Outcome, centered; actions pinned to the bottom edge like the form's CTA. */}
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', paddingHorizontal: spacing['2xl'] }}>
          <View
            style={{
              width: 80,
              height: 80,
              borderRadius: radius.pill,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
              backgroundColor: iconBg,
            }}
          >
            <Icon
              name={ok ? 'checkCircle' : pending ? 'clock' : 'closeCircle'}
              size={48}
              color={ok ? colors.success : pending ? colors.accent : colors.danger}
            />
          </View>
          <Text variant="title1" weight="700" align="center">
            {ok ? 'Payment sent' : pending ? 'Payment submitted' : 'Payment failed'}
          </Text>
          {ok || pending ? (
            <>
              <Text variant="title3" weight="700" numeric align="center" style={{ marginTop: spacing.sm }}>
                {amount} {selectedToken}
              </Text>
              <Text variant="bodySm" color="textSecondary" align="center" style={{ marginTop: spacing.xs }}>
                to @{resolvedUser?.username} on {activeChain?.name || selectedChain}
              </Text>
              <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.sm }}>
                {ok
                  ? 'Confirmed on-chain. It may take a moment to show in their wallet.'
                  : 'The network is taking longer than usual to confirm this transfer. Track it on the explorer — if it doesn’t confirm, contact support with the transaction hash below.'}
              </Text>
            </>
          ) : (
            <Card padding="md" style={{ marginTop: spacing.lg, alignSelf: 'stretch', backgroundColor: colors.dangerBg }}>
              <Text variant="bodySm" color="danger" align="center">
                {error || 'Something went wrong. Your funds have not moved.'}
              </Text>
            </Card>
          )}

          {txHash ? (
            <PressableScale
              onPress={async () => {
                await Clipboard.setStringAsync(txHash);
                setCopied(true);
                setTimeout(() => setCopied(false), 1500);
              }}
              accessibilityRole="button"
              accessibilityLabel="Copy transaction hash"
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                marginTop: spacing.xl,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.sm,
                borderRadius: radius.pill,
                backgroundColor: colors.surfaceAlt,
                maxWidth: '100%',
              }}
            >
              <Text variant="caption" color="textMuted" numeric numberOfLines={1} ellipsizeMode="middle" style={{ maxWidth: 200 }}>
                {txHash}
              </Text>
              <Icon name={copied ? 'checkCircle' : 'copy'} size={14} color={copied ? colors.success : colors.textMuted} />
            </PressableScale>
          ) : null}
          {explorerUrl ? (
            <PressableScale onPress={() => Linking.openURL(explorerUrl)} style={{ marginTop: spacing.sm }} hitSlop={8}>
              <Text variant="caption" weight="600" color="accent">
                View on explorer
              </Text>
            </PressableScale>
          ) : null}
        </View>

        <View style={{ padding: spacing.lg, gap: spacing.sm }}>
          <Button title="Back to Wallet" size="lg" onPress={() => router.replace('/tokenization')} />
          {ok ? (
            <Button
              title="Send again"
              variant="ghost"
              onPress={() => { setScreen('form'); setAmount(''); setUsername(''); setResolvedUser(null); }}
            />
          ) : (
            <Button title="Try again" variant="ghost" onPress={() => setScreen('form')} />
          )}
        </View>
      </SafeAreaView>
    );
  }

  // ── Sending ──
  if (screen === 'sending') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
        <View
          style={{
            width: 80,
            height: 80,
            borderRadius: radius.pill,
            backgroundColor: colors.accentMuted,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.lg,
          }}
        >
          <Spinner size="large" />
        </View>
        <Text variant="title1" weight="700" align="center">
          Sending payment
        </Text>
        <Text variant="bodySm" weight="600" color="accent" align="center" style={{ marginTop: spacing.xs }}>
          {amount} {selectedToken} to @{resolvedUser?.username}
        </Text>
        <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.sm }}>
          Waiting for the network to confirm — this can take up to a minute.
        </Text>
      </View>
    </SafeAreaView>
  );

  // ── Confirm ──
  if (screen === 'confirm') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <PressableScale
          onPress={() => setScreen('form')}
          style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.lg }}
        >
          <Icon name="chevronLeft" size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="600" color="accent">
            Back
          </Text>
        </PressableScale>
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>
          Confirm Payment
        </Text>

        {/* Recipient */}
        <AccentCard padding="md" style={{ marginBottom: spacing.md }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <Avatar name={resolvedUser?.name || resolvedUser?.username} size={44} />
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                {resolvedUser?.name}
              </Text>
              <Text variant="caption" color="textMuted">
                @{resolvedUser?.username}
              </Text>
            </View>
            {resolvedUser?.verificationStatus === 'verified' && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.successBg,
                  paddingHorizontal: spacing.sm,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.pill,
                }}
              >
                <Icon name="checkCircle" size={14} color={colors.success} />
                <Text variant="caption" weight="600" color="success">
                  Verified
                </Text>
              </View>
            )}
          </View>
        </AccentCard>

        {/* Details */}
        <Card>
          <SummaryCard>
            <SummaryRow
              label="Amount"
              value={
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <TokenBadge symbol={selectedToken} size={24} bg={tokenColorFor(selectedToken)} />
                  <Text variant="title3" weight="700" numeric>
                    {amount} {selectedToken}
                  </Text>
                </View>
              }
            />
            <Divider spacing="xs" />
            <SummaryRow label="Network" value={activeChain?.name || selectedChain} />
            <SummaryRow label="Gas Fee" value={`${gasEstimate} ${activeChain?.nativeCurrency?.symbol || 'S'}`} />
          </SummaryCard>
        </Card>

        <Button
          title="Confirm & Pay"
          leftIcon="bolt"
          onPress={handleSendRequest}
          size="lg"
          style={{ marginTop: spacing.lg }}
        />
        <Button
          title="Cancel"
          variant="ghost"
          onPress={() => setScreen('form')}
          style={{ marginTop: spacing.sm }}
        />
      </ScrollView>

      <AuthorizationModal
        visible={showAuthModal}
        title="Authorize Transfer"
        description={`Send ${amount} ${selectedToken} to @${resolvedUser?.username}`}
        onAuthorized={handleSend}
        onCancel={() => setShowAuthModal(false)}
      />
    </SafeAreaView>
  );

  // ── Form ──
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* Header */}
          <View style={{ alignItems: 'center', marginBottom: spacing['2xl'], paddingTop: spacing.sm }}>
            <View
              style={{
                width: 56,
                height: 56,
                borderRadius: radius.pill,
                backgroundColor: colors.accentMuted,
                justifyContent: 'center',
                alignItems: 'center',
                marginBottom: spacing.md,
              }}
            >
              <Icon name="bolt" size={28} color={colors.accent} />
            </View>
            <Text variant="title1" weight="700" align="center">
              Instant Pay
            </Text>
            <Text variant="bodySm" color="textMuted" align="center" style={{ marginTop: spacing.xs }}>
              Send crypto to any TSA Connect user
            </Text>
          </View>

          {/* Active ≠ registered wallet — sends still work (they leave the
              device wallet); incoming @username payments go to the registered
              wallet. Informational, never blocking. */}
          {walletMismatch ? (
            <AccentCard padding="md" style={{ marginBottom: spacing.md }}>
              <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
                <Icon name="info" size={16} color={colors.accent} />
                <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                  You&apos;re sending from this device&apos;s wallet ({shortAddr(activeWallet)}).
                  Payments sent to your @username arrive at your registered wallet ({shortAddr(registeredWallet)}).
                </Text>
              </View>
            </AccentCard>
          ) : null}

          {/* Recipient */}
          <Card style={{ marginBottom: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.md }}>
              RECIPIENT
            </Text>
            <Input
              placeholder="username"
              value={username}
              onChangeText={t => { setUsername(t.replace(/\s/g, '')); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
              leftAdornment={
                <Text variant="bodyMed" weight="700" color="accent">
                  @
                </Text>
              }
              rightAdornment={resolving ? <Spinner size="small" /> : undefined}
            />

            {resolvedUser && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                  marginTop: spacing.md,
                  backgroundColor: colors.successBg,
                  borderRadius: radius.md,
                  padding: spacing.sm,
                }}
              >
                <Avatar name={resolvedUser.name || resolvedUser.username} size={32} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodySm" weight="600">
                    {resolvedUser.name}
                  </Text>
                  <Text
                    variant="caption"
                    color="textMuted"
                    numeric
                    numberOfLines={1}
                    ellipsizeMode="middle"
                  >
                    {resolvedUser.walletAddress}
                  </Text>
                </View>
                {resolvedUser.verificationStatus === 'verified' && (
                  <Icon name="checkCircle" size={18} color={colors.success} />
                )}
              </View>
            )}
          </Card>

          {/* Amount + Token */}
          <Card style={{ marginBottom: spacing.md }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
              <Text variant="overline" color="textMuted">
                AMOUNT
              </Text>
              <PressableScale onPress={() => setAmount(bal)} hitSlop={6}>
                <Text variant="caption" weight="600" color="accent" numeric>
                  Balance: {bal}
                </Text>
              </PressableScale>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <PressableScale
                onPress={() => setShowTokenPicker(true)}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.surfaceAlt,
                  paddingHorizontal: spacing.sm,
                  paddingVertical: spacing.sm,
                  borderRadius: radius.pill,
                }}
              >
                <TokenBadge symbol={selectedToken} size={28} bg={tokenColorFor(selectedToken)} />
                <Text variant="bodyMed" weight="700">
                  {selectedToken}
                </Text>
                <Icon name="chevronDown" size={16} color={colors.textMuted} />
              </PressableScale>
              <View style={{ flex: 1 }}>
                <Input
                  placeholder="0.00"
                  value={amount}
                  onChangeText={t => { setAmount(t); setError(''); }}
                  keyboardType="decimal-pad"
                  inputStyle={{ textAlign: 'right' }}
                />
              </View>
            </View>
          </Card>

          {/* Error */}
          {error ? (
            <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg, marginBottom: spacing.sm }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="warning" size={16} color={colors.danger} />
                <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                  {error}
                </Text>
              </View>
            </AccentCard>
          ) : null}

          {/* Action */}
          <Button
            title="Review Payment"
            leftIcon="bolt"
            onPress={handleReview}
            loading={loading}
            disabled={loading || resolving}
            size="lg"
            style={{ marginTop: spacing.md }}
          />
        </ScrollView>

        {/* Token + Chain Picker */}
        <Sheet visible={showTokenPicker} onClose={() => setShowTokenPicker(false)} title="Select Token">
          <FlatList
            data={[...(balances as BalanceEntry[])].sort((a, b) => parseFloat(b.balance) - parseFloat(a.balance))}
            keyExtractor={item => `${item.symbol}-${item.chainKey}`}
            contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.lg }}
            keyboardShouldPersistTaps="handled"
            renderItem={({ item }) => {
              const active = item.symbol === selectedToken && item.chainKey === selectedChain;
              const balVal = parseFloat(item.balance);
              return (
                <PressableScale
                  onPress={() => {
                    setSelectedToken(item.symbol);
                    setSelectedChain(item.chainKey as ChainKey);
                    setShowTokenPicker(false);
                  }}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    paddingVertical: spacing.md,
                    paddingHorizontal: spacing.sm,
                    borderRadius: radius.md,
                    backgroundColor: active ? colors.accentMuted : 'transparent',
                  }}
                >
                  <TokenBadge symbol={item.symbol} size={36} bg={tokenColorFor(item.symbol)} />
                  <View style={{ flex: 1, marginLeft: spacing.md }}>
                    <Text variant="bodyMed" weight="700">
                      {item.symbol}
                    </Text>
                    <Text variant="caption" color="textMuted">
                      {item.name} · {item.chainName}
                    </Text>
                  </View>
                  <View style={{ alignItems: 'flex-end' }}>
                    <Text variant="bodySm" weight="600" numeric>
                      {balVal > 0 ? balVal.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0'}
                    </Text>
                    {parseFloat(item.usdValue) > 0 && (
                      <Text variant="caption" color="textMuted" numeric>
                        ${parseFloat(item.usdValue).toFixed(2)}
                      </Text>
                    )}
                  </View>
                </PressableScale>
              );
            }}
          />
        </Sheet>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default InstantPay;
