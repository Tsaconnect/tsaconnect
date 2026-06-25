// app/wallet/send.tsx
//
// Send crypto — recipient + amount + chain/token selection, then a
// review → authorize → sign + broadcast + confirm flow. Restyled onto the
// TSA Signature design system (@/theme tokens + @/components/ui primitives).
//
// FINANCIAL/SECURITY screen: this is a visual/structural restyle ONLY. Every
// piece of money handling is preserved verbatim — recipient-address parsing and
// validation (isValidAddress), amount parsing + the token↔USD input-mode
// conversion, chain/token selection (incl. the discovered-token nav-param path),
// gas estimation, and the prepareSendTransaction → signTransaction →
// submitTransaction confirm flow. No address or amount logic was altered.
import React, { useState, useEffect, useCallback } from 'react';
import {
  View, ScrollView, KeyboardAvoidingView, Platform, FlatList,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { isValidAddress, signTransaction } from '../../services/wallet';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  sendPreparedTransaction,
  submitTransaction,
  type WalletBalanceWithChain,
} from '../../services/walletApi';
import AuthorizationModal from '../../components/common/AuthorizationModal';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';

type BalanceEntry = WalletBalanceWithChain;

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

// Round token-symbol badge. Color is sourced from the token registry's
// iconColor (data-driven, like wallet/home) and falls back to a neutral token.
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

const SendToken = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokens, tokenList } = useTokens();

  // Token-color map derived from the registry (matches wallet/home), so badge
  // colors stay data-driven instead of hardcoded hex.
  const tokenColorFor = useCallback(
    (symbol: string): string => tokens[symbol]?.iconColor || colors.textMuted,
    [tokens, colors.textMuted],
  );

  // Discovered-token params (set when arriving from the wallet/token detail
  // screen for an auto-discovered ERC-20). When present, we skip the
  // per-chain balance scan and prepare the tx with explicit contract +
  // decimals, since the token isn't in supported_tokens.
  const navParams = useLocalSearchParams<{
    token?: string;
    chainKey?: string;
    contractAddress?: string;
    discovered?: string;
    decimals?: string;
    balance?: string;
    usdValue?: string;
    name?: string;
  }>();
  const isDiscovered = navParams.discovered === '1' && !!navParams.contractAddress;
  const discoveredDecimals = navParams.decimals ? parseInt(navParams.decimals, 10) : undefined;

  const [selectedToken, setSelectedToken] = useState(navParams.token || 'USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>(
    (navParams.chainKey as ChainKey) || 'sonic',
  );
  const [toAddress, setToAddress] = useState('');
  const [amount, setAmount] = useState('');
  const [inputMode, setInputMode] = useState<'token' | 'usd'>('token');
  const [balances, setBalances] = useState<BalanceEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);

  const getSupportedChainsForToken = useCallback((symbol: string): ChainKey[] => {
    const registryChains = tokens[symbol]?.chains ?? [];
    if (registryChains.length > 0) {
      return registryChains;
    }

    const inferredChains = Array.from(
      new Set(
        balances
          .filter((entry) => entry.symbol === symbol)
          .map((entry) => entry.chainKey)
      )
    );
    return inferredChains;
  }, [balances, tokens]);

  useEffect(() => {
    // Discovered tokens come pre-pinned to a single chain via nav params;
    // don't let the per-chain auto-correct overwrite that selection.
    if (isDiscovered) return;
    const chains = getSupportedChainsForToken(selectedToken);
    if (chains.length === 0) return;
    if (!chains.includes(selectedChain)) {
      setSelectedChain(chains[0]);
    }
  }, [getSupportedChainsForToken, selectedChain, selectedToken, isDiscovered]);

  const loadBalances = useCallback(async () => {
    // Discovered token: skip the multi-chain scan entirely (the token
    // isn't in supported_tokens, so it wouldn't appear) and seed a
    // single synthetic entry from the nav params.
    if (isDiscovered) {
      setBalances([{
        symbol: navParams.token || '',
        name: navParams.name || navParams.token || '',
        balance: navParams.balance || '0',
        usdValue: navParams.usdValue || '0',
        contractAddress: navParams.contractAddress || '',
        decimals: discoveredDecimals ?? 18,
        chainKey: (navParams.chainKey as ChainKey) || 'sonic',
        chainName: CHAINS[(navParams.chainKey as ChainKey) || 'sonic']?.shortName || (navParams.chainKey as string) || '',
      }]);
      setLoading(false);
      return;
    }
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

      // Add 0-balance entries for supported tokens not returned by API
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
      console.error('Send: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList, isDiscovered, navParams.token, navParams.name, navParams.balance, navParams.usdValue, navParams.contractAddress, navParams.chainKey, discoveredDecimals]);

  useEffect(() => { loadBalances(); }, [loadBalances]);

  const getBalance = (): string => {
    const entry = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
    return entry?.balance || '0';
  };
  const getUsdPrice = (): number => {
    const b = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
    if (!b) return selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0;
    const bal = parseFloat(b.balance || '0');
    const usd = parseFloat(b.usdValue || '0');
    return bal > 0 ? usd / bal : (selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0);
  };

  const tokenAmount = inputMode === 'token' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) / getUsdPrice()).toFixed(6) : '0'
  );
  const usdAmount = inputMode === 'usd' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) * getUsdPrice()).toFixed(2) : '0'
  );

  const activeChain = CHAINS[selectedChain];

  const handlePaste = async () => {
    try {
      const text = await Clipboard.getStringAsync();
      if (text) setToAddress(text.trim());
    } catch {}
  };

  const validate = (): string | null => {
    if (!toAddress.trim()) return 'Enter a recipient address.';
    if (!isValidAddress(toAddress.trim())) return 'Invalid wallet address.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    const sendAmount = parseFloat(tokenAmount);
    if (sendAmount > parseFloat(getBalance())) return 'Insufficient balance.';
    return null;
  };

  const handleReview = async () => {
    const e = validate(); if (e) { setError(e); return; }
    setError('');
    try {
      setLoading(true);
      const r = await prepareSendTransaction(
        selectedToken,
        toAddress.trim(),
        tokenAmount,
        activeChain?.chainId || CHAINS.sonic.chainId,
        isDiscovered
          ? {
              tokenAddress: navParams.contractAddress,
              decimals: discoveredDecimals,
            }
          : undefined,
      );
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      try {
        const gpStr = r.data.gasPrice || '0';
        const glStr = r.data.gasLimit || '21000';
        const gp = BigInt(gpStr);
        const gl = BigInt(glStr);
        const gasFee = Number(gp * gl) / 1e18;
        setGasEstimate(gasFee > 0 ? gasFee.toFixed(6) : '~0.001');
      } catch {
        setGasEstimate('~0.001');
      }
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSend = async () => {

    setScreen('sending'); setError('');
    const chainId = activeChain?.chainId || CHAINS.sonic.chainId;
    const recipient = toAddress.trim();
    try {
      // Nonce is computed server-side at prepare time and can go stale before
      // broadcast on Sonic's load-balanced RPC ("nonce too low"). The signed tx
      // can't be patched, so on a nonce error we re-prepare (fresh nonce),
      // re-sign, and retry once.
      const res = await sendPreparedTransaction({
        prepare: () =>
          prepareSendTransaction(
            selectedToken,
            recipient,
            tokenAmount,
            chainId,
            isDiscovered
              ? {
                  tokenAddress: navParams.contractAddress,
                  decimals: discoveredDecimals,
                }
              : undefined,
          ),
        sign: (tx) =>
          signTransaction({
            type: 0, // legacy transaction format
            to: tx.to, data: tx.data, value: tx.value,
            gasLimit: tx.gasLimit, gasPrice: tx.gasPrice,
            nonce: tx.nonce, chainId: tx.chainId,
          }),
        submit: (signed) =>
          submitTransaction(signed, 'send', selectedToken, recipient, tokenAmount, chainId),
      });
      if (res.success && res.data?.txHash) { setTxHash(res.data.txHash); setScreen('success'); }
      else throw new Error(res.message || 'Failed');
    } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
  };

  // ── Result ──
  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
          <View
            style={{
              width: 80,
              height: 80,
              borderRadius: radius.pill,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
              backgroundColor: ok ? colors.successBg : colors.dangerBg,
            }}
          >
            <Icon name={ok ? 'checkCircle' : 'closeCircle'} size={48} color={ok ? colors.success : colors.danger} />
          </View>
          <Text variant="title1" weight="700" align="center">
            {ok ? 'Sent!' : 'Failed'}
          </Text>
          <Text
            variant="bodySm"
            color="textSecondary"
            align="center"
            style={{ marginTop: spacing.xs, marginBottom: spacing.xs, paddingHorizontal: spacing['2xl'] }}
          >
            {ok ? `${tokenAmount} ${selectedToken} sent` : error}
          </Text>
          {ok && (
            <Text variant="bodySm" color="textMuted" align="center" style={{ marginBottom: spacing.xl }}>
              ≈ ${usdAmount}
            </Text>
          )}
          {txHash ? (
            <Text
              variant="caption"
              color="textMuted"
              numeric
              numberOfLines={1}
              ellipsizeMode="middle"
              style={{ maxWidth: '80%', marginBottom: spacing['2xl'] }}
            >
              Tx: {txHash}
            </Text>
          ) : null}
          <Button
            title="Back to Wallet"
            onPress={() => router.replace('/tokenization')}
            fullWidth={false}
            style={{ alignSelf: 'center', marginTop: spacing.lg }}
          />
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
          Sending
        </Text>
        <Text variant="bodySm" weight="600" color="accent" align="center" style={{ marginTop: spacing.xs }}>
          {tokenAmount} {selectedToken} → {toAddress.slice(0, 8)}...
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
          Confirm Send
        </Text>

        <Card>
          <SummaryCard>
            <SummaryRow
              label="Amount"
              value={
                <View style={{ alignItems: 'flex-end' }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <TokenBadge symbol={selectedToken} size={24} bg={tokenColorFor(selectedToken)} />
                    <Text variant="title3" weight="700" numeric>
                      {tokenAmount} {selectedToken}
                    </Text>
                  </View>
                  <Text variant="caption" color="textMuted" numeric style={{ marginTop: 2 }}>
                    ≈ ${usdAmount}
                  </Text>
                </View>
              }
            />
            <Divider spacing="xs" />
            <SummaryRow
              label="To"
              value={
                <Text variant="bodyMed" weight="600" numeric numberOfLines={1} ellipsizeMode="middle" style={{ maxWidth: 180 }}>
                  {toAddress}
                </Text>
              }
            />
            <SummaryRow label="Network" value={activeChain?.name || selectedChain} />
            <SummaryRow label="Gas Fee" value={`${gasEstimate} ${activeChain?.nativeCurrency?.symbol || 'S'}`} />
          </SummaryCard>
        </Card>

        <Button
          title="Confirm & Send"
          leftIcon="send"
          onPress={() => setShowAuthModal(true)}
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
        description={`Send ${tokenAmount} ${selectedToken} to ${toAddress.slice(0, 8)}...${toAddress.slice(-4)}`}
        onAuthorized={() => { setShowAuthModal(false); handleSend(); }}
        onCancel={() => setShowAuthModal(false)}
      />
    </SafeAreaView>
  );

  // ── Form ──
  const price = getUsdPrice();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Send
          </Text>
          <Text variant="bodySm" color="textMuted" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
            Transfer tokens to any wallet
          </Text>

          {/* Amount card */}
          <Card style={{ marginBottom: spacing.md }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
              <Text variant="overline" color="textMuted">
                AMOUNT
              </Text>
              <PressableScale
                onPress={() => {
                  setInputMode('token');
                  setAmount(getBalance());
                }}
                hitSlop={6}
              >
                <Text variant="caption" weight="600" color="accent" numeric>
                  Balance: {getBalance()} {selectedToken}
                </Text>
              </PressableScale>
            </View>

            {/* Token selector + Amount input */}
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
                <AmountInput
                  placeholder="0.00"
                  value={amount}
                  onChangeText={(t) => { setAmount(t); setError(''); }}
                  inputStyle={{ textAlign: 'right' }}
                />
              </View>
            </View>

            {/* USD equivalent + toggle */}
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginTop: spacing.md }}>
              <Text variant="bodySm" color="textMuted" numeric>
                {inputMode === 'token'
                  ? `≈ $${amount && price > 0 ? (parseFloat(amount) * price).toFixed(2) : '0.00'}`
                  : `≈ ${amount && price > 0 ? (parseFloat(amount) / price).toFixed(6) : '0'} ${selectedToken}`}
              </Text>
              <PressableScale
                onPress={() => {
                  if (inputMode === 'token' && price > 0 && amount) {
                    setAmount((parseFloat(amount) * price).toFixed(2));
                  } else if (inputMode === 'usd' && price > 0 && amount) {
                    setAmount((parseFloat(amount) / price).toFixed(6));
                  }
                  setInputMode(m => m === 'token' ? 'usd' : 'token');
                }}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.accentMuted,
                  paddingHorizontal: spacing.sm,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.pill,
                }}
              >
                <Icon name="swapVertical" size={16} color={colors.accent} />
                <Text variant="caption" weight="600" color="accent">
                  {inputMode === 'token' ? 'USD' : selectedToken}
                </Text>
              </PressableScale>
            </View>
          </Card>

          {/* Recipient */}
          <Card style={{ marginBottom: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.md }}>
              RECIPIENT
            </Text>
            <Input
              placeholder="0x... wallet address"
              value={toAddress}
              onChangeText={(t) => { setToAddress(t); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
              rightAdornment={
                <IconButton
                  icon="copy"
                  accessibilityLabel="Paste address"
                  color={colors.accent}
                  onPress={handlePaste}
                />
              }
              error={toAddress && !isValidAddress(toAddress) ? 'Invalid address format' : undefined}
            />
          </Card>

          {/* Network info */}
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.md, marginLeft: spacing.xs }}>
            <Icon name="info" size={16} color={colors.textMuted} />
            <Text variant="bodySm" color="textMuted">
              {activeChain?.name || selectedChain}
            </Text>
          </View>

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

          <Button
            title="Review"
            leftIcon="send"
            onPress={handleReview}
            loading={loading}
            disabled={loading}
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
              const bal = parseFloat(item.balance);
              return (
                <PressableScale
                  onPress={() => {
                    setSelectedToken(item.symbol);
                    setSelectedChain(item.chainKey);
                    setShowTokenPicker(false);
                    setInputMode('token');
                    setAmount('');
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
                      {bal > 0 ? bal.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0'}
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

export default SendToken;
